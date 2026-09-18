package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	dhcpPackage            = "dhcp"
	domainSectionType      = "domain"
	defaultApplyTimeoutSec = int64(10)
	sectionPrefix          = "tfdom_"
	sectionReadableMax     = 24
	sectionHashHexLen      = 16
	maxDomainLength        = 253
)

var validDomainChar = regexp.MustCompile(`^[a-z0-9._-]+$`)
var nonSectionChar = regexp.MustCompile(`[^a-z0-9_]+`)

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
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"ip": schema.StringAttribute{
				Required: true,
			},
		},
	}
}

func (r *Resource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*modernubus.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *modernubus.Client, got %T", req.ProviderData))
		return
	}
	r.client = client
}

func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	canonicalName, ok := validatePlan(plan, &resp.Diagnostics)
	if !ok {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}

	err := createDomain(ctx, r.client, canonicalName, plan.IP.ValueString(), defaultApplyTimeoutSec)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create domain", err.Error())
		return
	}

	state := ResourceModel{
		ID:   types.StringValue(canonicalName),
		Name: types.StringValue(canonicalName),
		IP:   types.StringValue(plan.IP.ValueString()),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
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

	canonicalName, err := canonicalDomainName(state.Name.ValueString())
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}

	live, exists, err := readLiveDomain(ctx, r.client, canonicalName)
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
	canonicalName, ok := validatePlan(plan, &resp.Diagnostics)
	if !ok {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}

	err := updateDomainIP(ctx, r.client, canonicalName, plan.IP.ValueString(), defaultApplyTimeoutSec)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update domain", err.Error())
		return
	}

	state := ResourceModel{
		ID:   types.StringValue(canonicalName),
		Name: types.StringValue(canonicalName),
		IP:   types.StringValue(plan.IP.ValueString()),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
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

	canonicalName, err := canonicalDomainName(state.Name.ValueString())
	if err != nil {
		return
	}
	if err := deleteDomain(ctx, r.client, canonicalName, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to delete domain", err.Error())
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	canonicalName, err := parseImportIdentifier(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import identifier", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), canonicalName)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), canonicalName)...)
}

func createDomain(ctx context.Context, client ubusDomainClient, domainName, ip string, applyTimeoutSec int64) error {
	section := sectionNameForDomain(domainName)
	minLifetime := time.Duration(applyTimeoutSec+5) * time.Second

	return client.RunMutationTransaction(ctx, minLifetime, func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: section})
		if err != nil {
			return err
		}
		if existing.SectionExists {
			return fmt.Errorf("section for domain already exists; import this resource using identifier %q", domainName)
		}

		addResp, err := client.UCIAdd(txCtx, modernubus.UCIAddRequest{
			Config: dhcpPackage,
			Type:   domainSectionType,
			Name:   section,
			Values: map[string]any{
				"name": domainName,
				"ip":   ip,
			},
		})
		if err != nil {
			return err
		}
		if addResp.Section != section {
			return fmt.Errorf("uci.add created unexpected section name")
		}

		if err := applyVerifyConfirm(txCtx, ctx, client, section, domainName, ip, applyTimeoutSec); err != nil {
			return err
		}
		return nil
	})
}

func updateDomainIP(ctx context.Context, client ubusDomainClient, domainName, ip string, applyTimeoutSec int64) error {
	section := sectionNameForDomain(domainName)
	minLifetime := time.Duration(applyTimeoutSec+5) * time.Second

	return client.RunMutationTransaction(ctx, minLifetime, func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: section})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return fmt.Errorf("managed section for domain is missing; run import or recreate")
		}

		if _, err := client.UCISet(txCtx, modernubus.UCISetRequest{
			Config:  dhcpPackage,
			Section: section,
			Values: map[string]any{
				"name": domainName,
				"ip":   ip,
			},
		}); err != nil {
			return err
		}

		return applyVerifyConfirm(txCtx, ctx, client, section, domainName, ip, applyTimeoutSec)
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

func applyVerifyConfirm(txCtx, healthCtx context.Context, client ubusDomainClient, section, expectedName, expectedIP string, applyTimeoutSec int64) error {
	if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{
		Rollback: true,
		Timeout:  applyTimeoutSec,
	}); err != nil {
		return err
	}
	if err := verifyRouterHealth(healthCtx, client); err != nil {
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
	if liveName != expectedName || liveIP != expectedIP {
		return fmt.Errorf("post-apply read-back mismatch")
	}
	_, err = client.UCIConfirm(txCtx, modernubus.UCIConfirmRequest{})
	return err
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

func validatePlan(plan ResourceModel, diags *diag.Diagnostics) (string, bool) {
	canonicalName, err := canonicalDomainName(plan.Name.ValueString())
	if err != nil {
		diags.AddError("Invalid domain name", err.Error())
	}
	if ip := net.ParseIP(strings.TrimSpace(plan.IP.ValueString())); ip == nil {
		diags.AddError("Invalid IP address", "ip must be a valid IPv4 or IPv6 address.")
	}
	return canonicalName, !diags.HasError()
}

func parseImportIdentifier(id string) (string, error) {
	canonical, err := canonicalDomainName(id)
	if err != nil {
		return "", fmt.Errorf("expected canonical domain name import identifier: %w", err)
	}
	return canonical, nil
}

func canonicalDomainName(name string) (string, error) {
	c := strings.ToLower(strings.TrimSpace(name))
	c = strings.TrimSuffix(c, ".")
	if c == "" {
		return "", fmt.Errorf("domain name cannot be empty")
	}
	if len(c) > maxDomainLength {
		return "", fmt.Errorf("domain name exceeds %d characters", maxDomainLength)
	}
	if !validDomainChar.MatchString(c) {
		return "", fmt.Errorf("domain name contains unsupported characters")
	}
	labels := strings.Split(c, ".")
	for _, label := range labels {
		if label == "" {
			return "", fmt.Errorf("domain name contains empty label")
		}
	}
	return c, nil
}

func sectionNameForDomain(name string) string {
	canonical, err := canonicalDomainName(name)
	if err != nil {
		canonical = "invalid"
	}
	readable := strings.ReplaceAll(canonical, ".", "_")
	readable = strings.ReplaceAll(readable, "-", "_")
	readable = nonSectionChar.ReplaceAllString(readable, "_")
	readable = strings.Trim(readable, "_")
	if readable == "" {
		readable = "domain"
	}
	if len(readable) > sectionReadableMax {
		readable = readable[:sectionReadableMax]
	}
	hash := sha256.Sum256([]byte(canonical))
	hashHex := hex.EncodeToString(hash[:])[:sectionHashHexLen]
	return sectionPrefix + readable + "_" + hashHex
}
