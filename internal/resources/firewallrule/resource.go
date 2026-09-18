package firewallrule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"slices"
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
	firewallPackage        = "firewall"
	ruleSectionType        = "rule"
	defaultApplyTimeoutSec = int64(10)
	sectionPrefix          = "tffwr_"
	sectionReadableMax     = 24
	sectionHashHexLen      = 16
	maxNameLength          = 63
)

var validNameChar = regexp.MustCompile(`^[a-z0-9._-]+$`)
var nonSectionChar = regexp.MustCompile(`[^a-z0-9_]+`)

type ubusFirewallRuleClient interface {
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
	client ubusFirewallRuleClient
}

type ResourceModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	Src      types.String `tfsdk:"src"`
	Dest     types.String `tfsdk:"dest"`
	Target   types.String `tfsdk:"target"`
	Proto    types.String `tfsdk:"proto"`
	Family   types.String `tfsdk:"family"`
	SrcPort  types.String `tfsdk:"src_port"`
	DestPort types.String `tfsdk:"dest_port"`
	Enabled  types.Bool   `tfsdk:"enabled"`
}

func NewResource() resource.Resource { return &Resource{} }

func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall_rule"
}

func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "OpenWrt firewall rule section in /etc/config/firewall.",
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
			"src":       schema.StringAttribute{Required: true},
			"dest":      schema.StringAttribute{Optional: true},
			"target":    schema.StringAttribute{Required: true},
			"proto":     schema.StringAttribute{Optional: true, Computed: true},
			"family":    schema.StringAttribute{Optional: true},
			"src_port":  schema.StringAttribute{Optional: true},
			"dest_port": schema.StringAttribute{Optional: true},
			"enabled":   schema.BoolAttribute{Optional: true, Computed: true},
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
	desired, ok := normalizePlan(plan, &resp.Diagnostics)
	if !ok {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}
	if err := createRule(ctx, r.client, desired, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to create firewall rule", err.Error())
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
	live, exists, err := readLiveRule(ctx, r.client, name)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read firewall rule", err.Error())
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
	desired, ok := normalizePlan(plan, &resp.Diagnostics)
	if !ok {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}
	if err := updateRule(ctx, r.client, desired, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to update firewall rule", err.Error())
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
	if err := deleteRule(ctx, r.client, name, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to delete firewall rule", err.Error())
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

func createRule(ctx context.Context, client ubusFirewallRuleClient, desired ResourceModel, applyTimeoutSec int64) error {
	name := desired.Name.ValueString()
	section := sectionNameForRule(name)
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: firewallPackage, Section: section})
		if err != nil {
			return err
		}
		if existing.SectionExists {
			return fmt.Errorf("section for firewall rule already exists; import this resource using identifier %q", name)
		}
		added, err := client.UCIAdd(txCtx, modernubus.UCIAddRequest{Config: firewallPackage, Type: ruleSectionType, Name: section, Values: ruleValues(desired)})
		if err != nil {
			return err
		}
		if added.Section != section {
			return fmt.Errorf("uci.add created unexpected section name")
		}
		return applyVerifyConfirm(txCtx, ctx, client, desired, applyTimeoutSec)
	})
}

func updateRule(ctx context.Context, client ubusFirewallRuleClient, desired ResourceModel, applyTimeoutSec int64) error {
	section := sectionNameForRule(desired.Name.ValueString())
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: firewallPackage, Section: section})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return fmt.Errorf("managed section for firewall rule is missing; run import or recreate")
		}
		for _, option := range optionalOptionsToDelete(existing, desired) {
			if _, err := client.UCIDelete(txCtx, modernubus.UCIDeleteRequest{Config: firewallPackage, Section: section, Option: option}); err != nil {
				return err
			}
		}
		if _, err := client.UCISet(txCtx, modernubus.UCISetRequest{Config: firewallPackage, Section: section, Values: ruleValues(desired)}); err != nil {
			return err
		}
		return applyVerifyConfirm(txCtx, ctx, client, desired, applyTimeoutSec)
	})
}

func deleteRule(ctx context.Context, client ubusFirewallRuleClient, name string, applyTimeoutSec int64) error {
	section := sectionNameForRule(name)
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: firewallPackage, Section: section})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return nil
		}
		if _, err := client.UCIDelete(txCtx, modernubus.UCIDeleteRequest{Config: firewallPackage, Section: section}); err != nil {
			return err
		}
		if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{Rollback: true, Timeout: applyTimeoutSec}); err != nil {
			return err
		}
		if err := verifyRouterHealth(ctx, client); err != nil {
			return err
		}
		readBack, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: firewallPackage, Section: section})
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

func applyVerifyConfirm(txCtx, healthCtx context.Context, client ubusFirewallRuleClient, expected ResourceModel, applyTimeoutSec int64) error {
	if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{Rollback: true, Timeout: applyTimeoutSec}); err != nil {
		return err
	}
	if err := verifyRouterHealth(healthCtx, client); err != nil {
		return err
	}
	live, exists, err := readLiveRule(txCtx, client, expected.Name.ValueString())
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

func readLiveRule(ctx context.Context, client ubusFirewallRuleClient, name string) (ResourceModel, bool, error) {
	section := sectionNameForRule(name)
	resp, err := client.UCIGet(ctx, modernubus.UCIGetRequest{Config: firewallPackage, Section: section})
	if err != nil {
		return ResourceModel{}, false, err
	}
	if !resp.SectionExists {
		return ResourceModel{}, false, nil
	}
	src, ok := resp.Values["src"].String()
	if !ok || strings.TrimSpace(src) == "" {
		return ResourceModel{}, false, fmt.Errorf("firewall rule section is missing required src option")
	}
	target, ok := resp.Values["target"].String()
	if !ok || strings.TrimSpace(target) == "" {
		return ResourceModel{}, false, fmt.Errorf("firewall rule section is missing required target option")
	}
	proto := "all"
	if raw, exists := resp.Values["proto"]; exists {
		if text, ok := raw.String(); ok && strings.TrimSpace(text) != "" {
			proto = strings.ToLower(strings.TrimSpace(text))
		}
	}
	model := ResourceModel{
		ID:       types.StringValue(name),
		Name:     types.StringValue(name),
		Src:      types.StringValue(strings.TrimSpace(src)),
		Target:   types.StringValue(strings.ToUpper(strings.TrimSpace(target))),
		Proto:    types.StringValue(proto),
		Enabled:  types.BoolValue(true),
		Dest:     types.StringNull(),
		Family:   types.StringNull(),
		SrcPort:  types.StringNull(),
		DestPort: types.StringNull(),
	}
	if raw, exists := resp.Values["dest"]; exists {
		if text, ok := raw.String(); ok && strings.TrimSpace(text) != "" {
			model.Dest = types.StringValue(strings.TrimSpace(text))
		}
	}
	if raw, exists := resp.Values["family"]; exists {
		if text, ok := raw.String(); ok && strings.TrimSpace(text) != "" {
			model.Family = types.StringValue(strings.ToLower(strings.TrimSpace(text)))
		}
	}
	if raw, exists := resp.Values["src_port"]; exists {
		if text, ok := raw.String(); ok && strings.TrimSpace(text) != "" {
			model.SrcPort = types.StringValue(strings.TrimSpace(text))
		}
	}
	if raw, exists := resp.Values["dest_port"]; exists {
		if text, ok := raw.String(); ok && strings.TrimSpace(text) != "" {
			model.DestPort = types.StringValue(strings.TrimSpace(text))
		}
	}
	if raw, exists := resp.Values["enabled"]; exists {
		if text, ok := raw.String(); ok {
			model.Enabled = types.BoolValue(parseUCIBool(text))
		}
	}
	return model, true, nil
}

func normalizePlan(plan ResourceModel, diags *diag.Diagnostics) (ResourceModel, bool) {
	name, err := canonicalName(plan.Name.ValueString())
	if err != nil {
		diags.AddError("Invalid firewall rule name", err.Error())
	}
	src := strings.TrimSpace(plan.Src.ValueString())
	if src == "" {
		diags.AddError("Invalid src", "src cannot be empty.")
	}
	target := strings.ToUpper(strings.TrimSpace(plan.Target.ValueString()))
	if target == "" {
		diags.AddError("Invalid target", "target cannot be empty.")
	}
	proto := "all"
	if !plan.Proto.IsNull() && !plan.Proto.IsUnknown() {
		proto = strings.ToLower(strings.TrimSpace(plan.Proto.ValueString()))
	}
	if proto == "" {
		diags.AddError("Invalid proto", "proto cannot be empty.")
	}
	enabled := types.BoolValue(true)
	if !plan.Enabled.IsNull() && !plan.Enabled.IsUnknown() {
		enabled = types.BoolValue(plan.Enabled.ValueBool())
	}

	if diags.HasError() {
		return ResourceModel{}, false
	}
	return ResourceModel{
		ID:       types.StringValue(name),
		Name:     types.StringValue(name),
		Src:      types.StringValue(src),
		Dest:     normalizeOptional(plan.Dest),
		Target:   types.StringValue(target),
		Proto:    types.StringValue(proto),
		Family:   normalizeOptionalLower(plan.Family),
		SrcPort:  normalizeOptional(plan.SrcPort),
		DestPort: normalizeOptional(plan.DestPort),
		Enabled:  enabled,
	}, true
}

func canonicalName(value string) (string, error) {
	canonical := strings.ToLower(strings.TrimSpace(value))
	if canonical == "" {
		return "", fmt.Errorf("name cannot be empty")
	}
	if len(canonical) > maxNameLength {
		return "", fmt.Errorf("name exceeds %d characters", maxNameLength)
	}
	if !validNameChar.MatchString(canonical) {
		return "", fmt.Errorf("name contains unsupported characters")
	}
	return canonical, nil
}

func normalizeOptional(value types.String) types.String {
	if value.IsNull() || value.IsUnknown() {
		return types.StringNull()
	}
	trimmed := strings.TrimSpace(value.ValueString())
	if trimmed == "" {
		return types.StringNull()
	}
	return types.StringValue(trimmed)
}

func normalizeOptionalLower(value types.String) types.String {
	normalized := normalizeOptional(value)
	if normalized.IsNull() {
		return normalized
	}
	return types.StringValue(strings.ToLower(normalized.ValueString()))
}

func sectionNameForRule(name string) string {
	canonical, err := canonicalName(name)
	if err != nil {
		canonical = "invalid"
	}
	readable := strings.ReplaceAll(canonical, ".", "_")
	readable = strings.ReplaceAll(readable, "-", "_")
	readable = nonSectionChar.ReplaceAllString(readable, "_")
	readable = strings.Trim(readable, "_")
	if readable == "" {
		readable = "rule"
	}
	if len(readable) > sectionReadableMax {
		readable = readable[:sectionReadableMax]
	}
	hash := sha256.Sum256([]byte(canonical))
	hashHex := hex.EncodeToString(hash[:])
	return sectionPrefix + readable + "_" + hashHex[:sectionHashHexLen]
}

func parseUCIBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}

func boolToUCIValue(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func optionalOptionsToDelete(existing modernubus.UCIGetResponse, desired ResourceModel) []string {
	optional := []struct {
		field  types.String
		option string
	}{
		{desired.Dest, "dest"},
		{desired.Family, "family"},
		{desired.SrcPort, "src_port"},
		{desired.DestPort, "dest_port"},
	}
	out := make([]string, 0, len(optional))
	for _, item := range optional {
		if item.field.IsNull() {
			if _, ok := existing.Values[item.option]; ok {
				out = append(out, item.option)
			}
		}
	}
	slices.Sort(out)
	return out
}

func ruleValues(model ResourceModel) map[string]any {
	values := map[string]any{
		"name":    model.Name.ValueString(),
		"src":     model.Src.ValueString(),
		"target":  model.Target.ValueString(),
		"proto":   model.Proto.ValueString(),
		"enabled": boolToUCIValue(model.Enabled.ValueBool()),
	}
	if !model.Dest.IsNull() && !model.Dest.IsUnknown() {
		values["dest"] = model.Dest.ValueString()
	}
	if !model.Family.IsNull() && !model.Family.IsUnknown() {
		values["family"] = model.Family.ValueString()
	}
	if !model.SrcPort.IsNull() && !model.SrcPort.IsUnknown() {
		values["src_port"] = model.SrcPort.ValueString()
	}
	if !model.DestPort.IsNull() && !model.DestPort.IsUnknown() {
		values["dest_port"] = model.DestPort.ValueString()
	}
	return values
}

func modelsEqual(left, right ResourceModel) bool {
	return left.ID.Equal(right.ID) &&
		left.Name.Equal(right.Name) &&
		left.Src.Equal(right.Src) &&
		left.Dest.Equal(right.Dest) &&
		left.Target.Equal(right.Target) &&
		left.Proto.Equal(right.Proto) &&
		left.Family.Equal(right.Family) &&
		left.SrcPort.Equal(right.SrcPort) &&
		left.DestPort.Equal(right.DestPort) &&
		left.Enabled.Equal(right.Enabled)
}

func transactionLifetime(applyTimeoutSec int64) time.Duration {
	return time.Duration(applyTimeoutSec+5) * time.Second
}

func verifyRouterHealth(ctx context.Context, client ubusFirewallRuleClient) error {
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
