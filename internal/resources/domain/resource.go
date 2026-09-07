package domain

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/h3ow3d/terraform-provider-openwrt/internal/client/modernubus"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	dhcpPackage            = "dhcp"
	domainSectionType      = "domain"
	defaultApplyTimeoutSec = int64(10)
)

type modernProviderData interface {
	ModernUBUS() *modernubus.Client
}

type ubusDomainClient interface {
	CurrentRPCURL() (string, error)
	EnsureSessionLifetime(ctx context.Context, minLifetime time.Duration) error
	RunMutationTransaction(ctx context.Context, minLifetime time.Duration, fn func(context.Context) error) error
	UCIGet(ctx context.Context, req modernubus.UCIGetRequest) (modernubus.UCIGetResponse, error)
	UCIAdd(ctx context.Context, req modernubus.UCIAddRequest) (modernubus.UCIAddResponse, error)
	UCISet(ctx context.Context, req modernubus.UCISetRequest) (modernubus.UCISetResponse, error)
	UCIDelete(ctx context.Context, req modernubus.UCIDeleteRequest) (modernubus.UCIDeleteResponse, error)
	UCIApply(ctx context.Context, req modernubus.UCIApplyRequest) (modernubus.UCIApplyResponse, error)
	UCIConfirm(ctx context.Context, req modernubus.UCIConfirmRequest) (modernubus.UCIConfirmResponse, error)
}

type Resource struct {
	client ubusDomainClient
}

type ResourceModel struct {
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
	IP   types.String `tfsdk:"ip"`
}

func NewResource() resource.Resource { return &Resource{} }

func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain"
}

func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Static domain record (config domain) in /etc/config/dhcp.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true},
			"name": schema.StringAttribute{Required: true},
			"ip":   schema.StringAttribute{Required: true},
		},
	}
}

func (r *Resource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	providerData, ok := req.ProviderData.(modernProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected provider data with modern ubus client, got %T", req.ProviderData))
		return
	}
	modern := providerData.ModernUBUS()
	if modern == nil {
		resp.Diagnostics.AddError("Missing modern ubus client", "Provider data did not include a modern ubus client for domain resource.")
		return
	}
	r.client = modern
}

func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !validatePlan(plan, &resp.Diagnostics) {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}

	if err := upsertDomain(ctx, r.client, plan, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to create domain", err.Error())
		return
	}
	plan.ID = types.StringValue(plan.Name.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}

	live, exists, err := readLiveDomain(ctx, r.client, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Failed to read domain", err.Error())
		return
	}
	if !exists {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &live)...)
}

func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !validatePlan(plan, &resp.Diagnostics) {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}

	if err := upsertDomain(ctx, r.client, plan, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to update domain", err.Error())
		return
	}
	plan.ID = types.StringValue(plan.Name.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *Resource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}

	if err := deleteDomain(ctx, r.client, state.Name.ValueString(), defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to delete domain", err.Error())
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func upsertDomain(ctx context.Context, client ubusDomainClient, plan ResourceModel, applyTimeoutSec int64) error {
	section := sectionNameForDomain(plan.Name.ValueString())
	minLifetime := time.Duration(applyTimeoutSec+5) * time.Second

	return client.RunMutationTransaction(ctx, minLifetime, func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: section})
		if err != nil {
			return err
		}
		values := map[string]any{
			"name": plan.Name.ValueString(),
			"ip":   plan.IP.ValueString(),
		}

		if !existing.SectionExists {
			addResp, err := client.UCIAdd(txCtx, modernubus.UCIAddRequest{
				Config: dhcpPackage,
				Type:   domainSectionType,
				Name:   section,
				Values: values,
			})
			if err != nil {
				return err
			}
			if addResp.Section != section {
				return fmt.Errorf("uci.add created unexpected section name")
			}
		} else {
			if _, err := client.UCISet(txCtx, modernubus.UCISetRequest{
				Config:  dhcpPackage,
				Section: section,
				Values:  values,
			}); err != nil {
				return err
			}
		}

		if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{
			Rollback: true,
			Timeout:  applyTimeoutSec,
		}); err != nil {
			return err
		}
		if err := verifyRouterHealth(ctx, client); err != nil {
			return err
		}

		readBack, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: section})
		if err != nil {
			return err
		}
		if !readBack.SectionExists {
			return fmt.Errorf("post-apply read-back missing section")
		}
		liveName, _ := readBack.Values["name"].String()
		liveIP, _ := readBack.Values["ip"].String()
		if liveName != plan.Name.ValueString() || liveIP != plan.IP.ValueString() {
			return fmt.Errorf("post-apply read-back mismatch")
		}

		_, err = client.UCIConfirm(txCtx, modernubus.UCIConfirmRequest{})
		return err
	})
}

func deleteDomain(ctx context.Context, client ubusDomainClient, domainName string, applyTimeoutSec int64) error {
	section := sectionNameForDomain(domainName)
	minLifetime := time.Duration(applyTimeoutSec+5) * time.Second

	return client.RunMutationTransaction(ctx, minLifetime, func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: section})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return nil
		}

		if _, err := client.UCIDelete(txCtx, modernubus.UCIDeleteRequest{
			Config:  dhcpPackage,
			Section: section,
		}); err != nil {
			return err
		}

		if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{
			Rollback: true,
			Timeout:  applyTimeoutSec,
		}); err != nil {
			return err
		}
		if err := verifyRouterHealth(ctx, client); err != nil {
			return err
		}

		readBack, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: section})
		if err != nil {
			return err
		}
		if readBack.SectionExists {
			return fmt.Errorf("post-delete apply read-back still found section")
		}

		_, err = client.UCIConfirm(txCtx, modernubus.UCIConfirmRequest{})
		return err
	})
}

func readLiveDomain(ctx context.Context, client ubusDomainClient, domainName string) (ResourceModel, bool, error) {
	section := sectionNameForDomain(domainName)
	resp, err := client.UCIGet(ctx, modernubus.UCIGetRequest{
		Config:  dhcpPackage,
		Section: section,
	})
	if err != nil {
		return ResourceModel{}, false, err
	}
	if !resp.SectionExists {
		return ResourceModel{}, false, nil
	}
	name, nameOK := resp.Values["name"].String()
	ip, ipOK := resp.Values["ip"].String()
	if !nameOK || !ipOK {
		return ResourceModel{}, false, fmt.Errorf("domain section is missing required options")
	}
	model := ResourceModel{
		ID:   types.StringValue(name),
		Name: types.StringValue(name),
		IP:   types.StringValue(ip),
	}
	return model, true, nil
}

func verifyRouterHealth(ctx context.Context, client ubusDomainClient) error {
	rpcURL, err := client.CurrentRPCURL()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rpcURL, nil)
	if err != nil {
		return err
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("router health check failed: http %d", resp.StatusCode)
	}
	return nil
}

func sectionNameForDomain(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.ReplaceAll(normalized, ".", "_")
	normalized = regexp.MustCompile(`[^a-z0-9_]`).ReplaceAllString(normalized, "_")
	if normalized == "" {
		normalized = "empty"
	}
	return "tf_domain_" + normalized
}

func validatePlan(plan ResourceModel, diags *diag.Diagnostics) bool {
	if strings.TrimSpace(plan.Name.ValueString()) == "" {
		diags.AddError("Invalid domain name", "name cannot be empty.")
	}
	if ip := net.ParseIP(strings.TrimSpace(plan.IP.ValueString())); ip == nil {
		diags.AddError("Invalid IP address", "ip must be a valid IPv4 or IPv6 address.")
	}
	return !diags.HasError()
}
