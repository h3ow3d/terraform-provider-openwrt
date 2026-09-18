package dhcppool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
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
	poolSectionType        = "dhcp"
	defaultApplyTimeoutSec = int64(10)
	sectionPrefix          = "tfpool_"
	sectionReadableMax     = 24
	sectionHashHexLen      = 16
	maxNameLength          = 253
)

var validNameChar = regexp.MustCompile(`^[a-z0-9._-]+$`)
var nonSectionChar = regexp.MustCompile(`[^a-z0-9_]+`)

type ubusPoolClient interface {
	CurrentRPCURL() (string, error)
	RunMutationTransaction(ctx context.Context, minLifetime time.Duration, fn func(context.Context) error) error
	UCIGet(ctx context.Context, req modernubus.UCIGetRequest) (modernubus.UCIGetResponse, error)
	UCIAdd(ctx context.Context, req modernubus.UCIAddRequest) (modernubus.UCIAddResponse, error)
	UCISet(ctx context.Context, req modernubus.UCISetRequest) (modernubus.UCISetResponse, error)
	UCIDelete(ctx context.Context, req modernubus.UCIDeleteRequest) (modernubus.UCIDeleteResponse, error)
	UCIApply(ctx context.Context, req modernubus.UCIApplyRequest) (modernubus.UCIApplyResponse, error)
	UCIConfirm(ctx context.Context, req modernubus.UCIConfirmRequest) (modernubus.UCIConfirmResponse, error)
}

type Resource struct {
	client ubusPoolClient
}

type ResourceModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Interface types.String `tfsdk:"interface"`
	Start     types.Int64  `tfsdk:"start"`
	Limit     types.Int64  `tfsdk:"limit"`
	LeaseTime types.String `tfsdk:"leasetime"`
	Force     types.Bool   `tfsdk:"force"`
	DHCPv6    types.String `tfsdk:"dhcpv6"`
	RA        types.String `tfsdk:"ra"`
}

func NewResource() resource.Resource { return &Resource{} }

func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dhcp_pool"
}

func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "DHCP pool for an interface in /etc/config/dhcp.",
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
			"interface": schema.StringAttribute{Required: true},
			"start":     schema.Int64Attribute{Required: true},
			"limit":     schema.Int64Attribute{Required: true},
			"leasetime": schema.StringAttribute{Optional: true, Computed: true},
			"force":     schema.BoolAttribute{Optional: true, Computed: true},
			"dhcpv6":    schema.StringAttribute{Optional: true, Computed: true},
			"ra":        schema.StringAttribute{Optional: true, Computed: true},
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
	applyDefaults(&plan)
	desired, ok := normalizePlan(plan, &resp.Diagnostics)
	if !ok {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}
	if err := createPool(ctx, r.client, desired, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to create DHCP pool", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &desired)...)
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
	name, err := canonicalName(state.Name.ValueString())
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}
	live, exists, err := readLivePool(ctx, r.client, name)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read DHCP pool", err.Error())
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
	applyDefaults(&plan)
	desired, ok := normalizePlan(plan, &resp.Diagnostics)
	if !ok {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}
	if err := updatePool(ctx, r.client, desired, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to update DHCP pool", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &desired)...)
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
	name, err := canonicalName(state.Name.ValueString())
	if err != nil {
		return
	}
	if err := deletePool(ctx, r.client, name, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to delete DHCP pool", err.Error())
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	name, err := canonicalName(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import identifier", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

func createPool(ctx context.Context, client ubusPoolClient, desired ResourceModel, applyTimeoutSec int64) error {
	name := desired.Name.ValueString()
	section := sectionNameForPool(name)
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: section})
		if err != nil {
			return err
		}
		if existing.SectionExists {
			return fmt.Errorf("section for DHCP pool already exists; import this resource using identifier %q", name)
		}
		addResp, err := client.UCIAdd(txCtx, modernubus.UCIAddRequest{
			Config: dhcpPackage,
			Type:   poolSectionType,
			Name:   section,
			Values: poolValues(desired),
		})
		if err != nil {
			return err
		}
		if addResp.Section != section {
			return fmt.Errorf("uci.add created unexpected section name")
		}
		return applyVerifyConfirm(txCtx, ctx, client, desired, applyTimeoutSec)
	})
}

func updatePool(ctx context.Context, client ubusPoolClient, desired ResourceModel, applyTimeoutSec int64) error {
	section := sectionNameForPool(desired.Name.ValueString())
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: section})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return fmt.Errorf("managed section for DHCP pool is missing; run import or recreate")
		}
		if _, err := client.UCISet(txCtx, modernubus.UCISetRequest{Config: dhcpPackage, Section: section, Values: poolValues(desired)}); err != nil {
			return err
		}
		return applyVerifyConfirm(txCtx, ctx, client, desired, applyTimeoutSec)
	})
}

func deletePool(ctx context.Context, client ubusPoolClient, name string, applyTimeoutSec int64) error {
	section := sectionNameForPool(name)
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: section})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return nil
		}
		if _, err := client.UCIDelete(txCtx, modernubus.UCIDeleteRequest{Config: dhcpPackage, Section: section}); err != nil {
			return err
		}
		if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{Rollback: true, Timeout: applyTimeoutSec}); err != nil {
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

func applyVerifyConfirm(txCtx, healthCtx context.Context, client ubusPoolClient, expected ResourceModel, applyTimeoutSec int64) error {
	if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{Rollback: true, Timeout: applyTimeoutSec}); err != nil {
		return err
	}
	if err := verifyRouterHealth(healthCtx, client); err != nil {
		return err
	}
	live, exists, err := readLivePool(txCtx, client, expected.Name.ValueString())
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("post-apply read-back missing section")
	}
	if !modelsEqual(live, expected) {
		return fmt.Errorf("post-apply read-back mismatch")
	}
	_, err = client.UCIConfirm(txCtx, modernubus.UCIConfirmRequest{})
	return err
}

func readLivePool(ctx context.Context, client ubusPoolClient, name string) (ResourceModel, bool, error) {
	resp, err := client.UCIGet(ctx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: sectionNameForPool(name)})
	if err != nil {
		return ResourceModel{}, false, err
	}
	if !resp.SectionExists {
		return ResourceModel{}, false, nil
	}
	interfaceName, ok := resp.Values["interface"].String()
	if !ok || strings.TrimSpace(interfaceName) == "" {
		return ResourceModel{}, false, fmt.Errorf("DHCP pool section is missing required interface option")
	}
	start, err := readIntOption(resp.Values, "start")
	if err != nil {
		return ResourceModel{}, false, err
	}
	limit, err := readIntOption(resp.Values, "limit")
	if err != nil {
		return ResourceModel{}, false, err
	}
	model := ResourceModel{
		ID:        types.StringValue(name),
		Name:      types.StringValue(name),
		Interface: types.StringValue(strings.TrimSpace(interfaceName)),
		Start:     types.Int64Value(start),
		Limit:     types.Int64Value(limit),
		LeaseTime: readStringOptionWithDefault(resp.Values, "leasetime", "12h"),
		Force:     types.BoolValue(readBoolOption(resp.Values, "force", true)),
		DHCPv6:    readStringOptionWithDefault(resp.Values, "dhcpv6", "disabled"),
		RA:        readStringOptionWithDefault(resp.Values, "ra", "disabled"),
	}
	return model, true, nil
}

func applyDefaults(plan *ResourceModel) {
	if plan.LeaseTime.IsNull() || plan.LeaseTime.IsUnknown() || strings.TrimSpace(plan.LeaseTime.ValueString()) == "" {
		plan.LeaseTime = types.StringValue("12h")
	}
	if plan.Force.IsNull() || plan.Force.IsUnknown() {
		plan.Force = types.BoolValue(true)
	}
	if plan.DHCPv6.IsNull() || plan.DHCPv6.IsUnknown() || strings.TrimSpace(plan.DHCPv6.ValueString()) == "" {
		plan.DHCPv6 = types.StringValue("disabled")
	}
	if plan.RA.IsNull() || plan.RA.IsUnknown() || strings.TrimSpace(plan.RA.ValueString()) == "" {
		plan.RA = types.StringValue("disabled")
	}
}

func normalizePlan(plan ResourceModel, diags *diag.Diagnostics) (ResourceModel, bool) {
	name, err := canonicalName(plan.Name.ValueString())
	if err != nil {
		diags.AddError("Invalid DHCP pool name", err.Error())
	}
	interfaceName := strings.TrimSpace(plan.Interface.ValueString())
	if interfaceName == "" {
		diags.AddError("Invalid interface", "interface cannot be empty.")
	}
	start := plan.Start.ValueInt64()
	if start < 0 {
		diags.AddError("Invalid start", "start must be zero or greater.")
	}
	limit := plan.Limit.ValueInt64()
	if limit <= 0 {
		diags.AddError("Invalid limit", "limit must be greater than zero.")
	}
	leasetime := strings.TrimSpace(plan.LeaseTime.ValueString())
	if leasetime == "" {
		diags.AddError("Invalid leasetime", "leasetime cannot be empty.")
	}
	dhcpv6 := strings.TrimSpace(plan.DHCPv6.ValueString())
	if dhcpv6 == "" {
		diags.AddError("Invalid dhcpv6", "dhcpv6 cannot be empty.")
	}
	ra := strings.TrimSpace(plan.RA.ValueString())
	if ra == "" {
		diags.AddError("Invalid ra", "ra cannot be empty.")
	}
	if diags.HasError() {
		return ResourceModel{}, false
	}
	return ResourceModel{
		ID:        types.StringValue(name),
		Name:      types.StringValue(name),
		Interface: types.StringValue(interfaceName),
		Start:     types.Int64Value(start),
		Limit:     types.Int64Value(limit),
		LeaseTime: types.StringValue(leasetime),
		Force:     types.BoolValue(plan.Force.ValueBool()),
		DHCPv6:    types.StringValue(dhcpv6),
		RA:        types.StringValue(ra),
	}, true
}

func canonicalName(value string) (string, error) {
	canonical := strings.ToLower(strings.TrimSpace(value))
	canonical = strings.TrimSuffix(canonical, ".")
	if canonical == "" {
		return "", fmt.Errorf("name cannot be empty")
	}
	if len(canonical) > maxNameLength {
		return "", fmt.Errorf("name exceeds %d characters", maxNameLength)
	}
	if !validNameChar.MatchString(canonical) {
		return "", fmt.Errorf("name contains unsupported characters")
	}
	for _, label := range strings.Split(canonical, ".") {
		if label == "" {
			return "", fmt.Errorf("name contains an empty label")
		}
	}
	return canonical, nil
}

func sectionNameForPool(name string) string {
	canonical, err := canonicalName(name)
	if err != nil {
		canonical = "invalid"
	}
	readable := strings.ReplaceAll(canonical, ".", "_")
	readable = strings.ReplaceAll(readable, "-", "_")
	readable = nonSectionChar.ReplaceAllString(readable, "_")
	readable = strings.Trim(readable, "_")
	if readable == "" {
		readable = "pool"
	}
	if len(readable) > sectionReadableMax {
		readable = readable[:sectionReadableMax]
	}
	hash := sha256.Sum256([]byte(canonical))
	return sectionPrefix + readable + "_" + hex.EncodeToString(hash[:])[:sectionHashHexLen]
}

func poolValues(model ResourceModel) map[string]any {
	values := map[string]any{
		"interface": model.Interface.ValueString(),
		"start":     strconv.FormatInt(model.Start.ValueInt64(), 10),
		"limit":     strconv.FormatInt(model.Limit.ValueInt64(), 10),
		"leasetime": model.LeaseTime.ValueString(),
		"force":     boolOption(model.Force.ValueBool()),
		"dhcpv6":    model.DHCPv6.ValueString(),
		"ra":        model.RA.ValueString(),
	}
	return values
}

func readIntOption(values map[string]modernubus.UCIValue, key string) (int64, error) {
	raw, ok := values[key]
	if !ok {
		return 0, fmt.Errorf("DHCP pool section is missing required %s option", key)
	}
	text, ok := raw.String()
	if !ok {
		return 0, fmt.Errorf("DHCP pool %s option is not a string", key)
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("DHCP pool %s option is not a valid integer", key)
	}
	return parsed, nil
}

func readStringOptionWithDefault(values map[string]modernubus.UCIValue, key, defaultValue string) types.String {
	raw, ok := values[key]
	if !ok {
		return types.StringValue(defaultValue)
	}
	text, ok := raw.String()
	if !ok || strings.TrimSpace(text) == "" {
		return types.StringValue(defaultValue)
	}
	return types.StringValue(strings.TrimSpace(text))
}

func readBoolOption(values map[string]modernubus.UCIValue, key string, defaultValue bool) bool {
	raw, ok := values[key]
	if !ok {
		return defaultValue
	}
	text, ok := raw.String()
	if !ok {
		return defaultValue
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "0" || strings.EqualFold(trimmed, "false") {
		return false
	}
	if trimmed == "1" || strings.EqualFold(trimmed, "true") {
		return true
	}
	return defaultValue
}

func boolOption(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func modelsEqual(left, right ResourceModel) bool {
	return left.ID.Equal(right.ID) &&
		left.Name.Equal(right.Name) &&
		left.Interface.Equal(right.Interface) &&
		left.Start.Equal(right.Start) &&
		left.Limit.Equal(right.Limit) &&
		left.LeaseTime.Equal(right.LeaseTime) &&
		left.Force.Equal(right.Force) &&
		left.DHCPv6.Equal(right.DHCPv6) &&
		left.RA.Equal(right.RA)
}

func transactionLifetime(applyTimeoutSec int64) time.Duration {
	return time.Duration(applyTimeoutSec+5) * time.Second
}

func verifyRouterHealth(ctx context.Context, client ubusPoolClient) error {
	rpcURL, err := client.CurrentRPCURL()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rpcURL, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("router health check failed: http %d", resp.StatusCode)
	}
	return nil
}
