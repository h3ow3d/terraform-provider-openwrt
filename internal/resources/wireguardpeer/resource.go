package wireguardpeer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"slices"
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
	networkPackage         = "network"
	defaultApplyTimeoutSec = int64(10)
	sectionPrefix          = "tfwgp_"
	sectionReadableMax     = 24
	sectionHashHexLen      = 16
	maxNameLength          = 63
)

var validNameChar = regexp.MustCompile(`^[a-z0-9._-]+$`)
var validInterfaceChar = regexp.MustCompile(`^[a-z0-9_]+$`)
var nonSectionChar = regexp.MustCompile(`[^a-z0-9_]+`)

type ubusWireGuardPeerClient interface {
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
	client ubusWireGuardPeerClient
}

type ResourceModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	Interface           types.String `tfsdk:"interface"`
	PublicKey           types.String `tfsdk:"public_key"`
	AllowedIPs          types.List   `tfsdk:"allowed_ips"`
	PresharedKey        types.String `tfsdk:"preshared_key"`
	EndpointHost        types.String `tfsdk:"endpoint_host"`
	EndpointPort        types.Int64  `tfsdk:"endpoint_port"`
	PersistentKeepalive types.Int64  `tfsdk:"persistent_keepalive"`
	Description         types.String `tfsdk:"description"`
}

func NewResource() resource.Resource { return &Resource{} }

func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_wireguard_peer"
}

func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "WireGuard peer section in /etc/config/network.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"interface": schema.StringAttribute{
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"public_key":           schema.StringAttribute{Required: true},
			"allowed_ips":          schema.ListAttribute{Required: true, ElementType: types.StringType},
			"preshared_key":        schema.StringAttribute{Optional: true, Sensitive: true},
			"endpoint_host":        schema.StringAttribute{Optional: true},
			"endpoint_port":        schema.Int64Attribute{Optional: true},
			"persistent_keepalive": schema.Int64Attribute{Optional: true},
			"description":          schema.StringAttribute{Optional: true},
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
	desired, ok := normalizePlan(ctx, plan, &resp.Diagnostics)
	if !ok {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}
	if err := createPeer(ctx, r.client, desired, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to create WireGuard peer", err.Error())
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
	iface, err := canonicalInterface(state.Interface.ValueString())
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}
	live, exists, err := readLivePeer(ctx, r.client, name, iface)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read WireGuard peer", err.Error())
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
	desired, ok := normalizePlan(ctx, plan, &resp.Diagnostics)
	if !ok {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}
	if err := updatePeer(ctx, r.client, desired, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to update WireGuard peer", err.Error())
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
	iface, err := canonicalInterface(state.Interface.ValueString())
	if err != nil {
		return
	}
	if err := deletePeer(ctx, r.client, name, iface, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to delete WireGuard peer", err.Error())
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(strings.TrimSpace(req.ID), "/", 2)
	if len(parts) != 2 {
		resp.Diagnostics.AddError("Invalid import identifier", "expected import id in form <interface>/<name>")
		return
	}
	iface, err := canonicalInterface(parts[0])
	if err != nil {
		resp.Diagnostics.AddError("Invalid import identifier", err.Error())
		return
	}
	name, err := canonicalName(parts[1])
	if err != nil {
		resp.Diagnostics.AddError("Invalid import identifier", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("interface"), iface)...)
}

func createPeer(ctx context.Context, client ubusWireGuardPeerClient, desired ResourceModel, applyTimeoutSec int64) error {
	name := desired.Name.ValueString()
	iface := desired.Interface.ValueString()
	section := sectionNameForPeer(iface, name)
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: networkPackage, Section: section})
		if err != nil {
			return err
		}
		if existing.SectionExists {
			return fmt.Errorf("section for WireGuard peer already exists; import this resource using identifier %q", iface+"/"+name)
		}
		added, err := client.UCIAdd(txCtx, modernubus.UCIAddRequest{Config: networkPackage, Type: peerSectionType(iface), Name: section, Values: peerValues(desired)})
		if err != nil {
			return err
		}
		if added.Section != section {
			return fmt.Errorf("uci.add created unexpected section name")
		}
		return applyVerifyConfirm(txCtx, ctx, client, desired, applyTimeoutSec)
	})
}

func updatePeer(ctx context.Context, client ubusWireGuardPeerClient, desired ResourceModel, applyTimeoutSec int64) error {
	iface := desired.Interface.ValueString()
	section := sectionNameForPeer(iface, desired.Name.ValueString())
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: networkPackage, Section: section})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return fmt.Errorf("managed section for WireGuard peer is missing; run import or recreate")
		}
		for _, option := range optionalOptionsToDelete(existing, desired) {
			if _, err := client.UCIDelete(txCtx, modernubus.UCIDeleteRequest{Config: networkPackage, Section: section, Option: option}); err != nil {
				return err
			}
		}
		if _, err := client.UCISet(txCtx, modernubus.UCISetRequest{Config: networkPackage, Section: section, Values: peerValues(desired)}); err != nil {
			return err
		}
		return applyVerifyConfirm(txCtx, ctx, client, desired, applyTimeoutSec)
	})
}

func deletePeer(ctx context.Context, client ubusWireGuardPeerClient, name, iface string, applyTimeoutSec int64) error {
	section := sectionNameForPeer(iface, name)
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: networkPackage, Section: section})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return nil
		}
		if _, err := client.UCIDelete(txCtx, modernubus.UCIDeleteRequest{Config: networkPackage, Section: section}); err != nil {
			return err
		}
		if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{Rollback: true, Timeout: applyTimeoutSec}); err != nil {
			return err
		}
		if err := verifyRouterHealth(ctx, client); err != nil {
			return err
		}
		readBack, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: networkPackage, Section: section})
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

func applyVerifyConfirm(txCtx, healthCtx context.Context, client ubusWireGuardPeerClient, expected ResourceModel, applyTimeoutSec int64) error {
	if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{Rollback: true, Timeout: applyTimeoutSec}); err != nil {
		return err
	}
	if err := verifyRouterHealth(healthCtx, client); err != nil {
		return err
	}
	live, exists, err := readLivePeer(txCtx, client, expected.Name.ValueString(), expected.Interface.ValueString())
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

func readLivePeer(ctx context.Context, client ubusWireGuardPeerClient, name, iface string) (ResourceModel, bool, error) {
	section := sectionNameForPeer(iface, name)
	resp, err := client.UCIGet(ctx, modernubus.UCIGetRequest{Config: networkPackage, Section: section})
	if err != nil {
		return ResourceModel{}, false, err
	}
	if !resp.SectionExists {
		return ResourceModel{}, false, nil
	}
	if sectionType, ok := resp.Values[".type"].String(); ok && strings.TrimSpace(sectionType) != peerSectionType(iface) {
		return ResourceModel{}, false, fmt.Errorf("section exists but is not a WireGuard peer for interface %q", iface)
	}
	publicKey, ok := resp.Values["public_key"].String()
	if !ok || strings.TrimSpace(publicKey) == "" {
		return ResourceModel{}, false, fmt.Errorf("WireGuard peer section is missing required public_key option")
	}
	allowedIPs, err := allowedIPsFromValues(ctx, resp.Values)
	if err != nil {
		return ResourceModel{}, false, err
	}
	if allowedIPs.IsNull() {
		return ResourceModel{}, false, fmt.Errorf("WireGuard peer section is missing required allowed_ips option")
	}
	model := ResourceModel{
		ID:                  types.StringValue(name),
		Name:                types.StringValue(name),
		Interface:           types.StringValue(iface),
		PublicKey:           types.StringValue(strings.TrimSpace(publicKey)),
		AllowedIPs:          allowedIPs,
		PresharedKey:        types.StringNull(),
		EndpointHost:        types.StringNull(),
		EndpointPort:        types.Int64Null(),
		PersistentKeepalive: types.Int64Null(),
		Description:         types.StringNull(),
	}
	if raw, exists := resp.Values["preshared_key"]; exists {
		if text, ok := raw.String(); ok && strings.TrimSpace(text) != "" {
			model.PresharedKey = types.StringValue(strings.TrimSpace(text))
		}
	}
	if raw, exists := resp.Values["endpoint_host"]; exists {
		if text, ok := raw.String(); ok && strings.TrimSpace(text) != "" {
			model.EndpointHost = types.StringValue(strings.TrimSpace(text))
		}
	}
	if raw, exists := resp.Values["endpoint_port"]; exists {
		if text, ok := raw.String(); ok {
			parsed, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
			if err == nil && parsed > 0 && parsed <= 65535 {
				model.EndpointPort = types.Int64Value(parsed)
			}
		}
	}
	if raw, exists := resp.Values["persistent_keepalive"]; exists {
		if text, ok := raw.String(); ok {
			parsed, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
			if err == nil && parsed >= 0 && parsed <= 65535 {
				model.PersistentKeepalive = types.Int64Value(parsed)
			}
		}
	}
	if raw, exists := resp.Values["description"]; exists {
		if text, ok := raw.String(); ok && strings.TrimSpace(text) != "" {
			model.Description = types.StringValue(strings.TrimSpace(text))
		}
	}
	return model, true, nil
}

func normalizePlan(ctx context.Context, plan ResourceModel, diags *diag.Diagnostics) (ResourceModel, bool) {
	name, err := canonicalName(plan.Name.ValueString())
	if err != nil {
		diags.AddError("Invalid WireGuard peer name", err.Error())
	}
	iface, err := canonicalInterface(plan.Interface.ValueString())
	if err != nil {
		diags.AddError("Invalid interface", err.Error())
	}
	publicKey := strings.TrimSpace(plan.PublicKey.ValueString())
	if publicKey == "" {
		diags.AddError("Invalid public_key", "public_key cannot be empty.")
	}
	allowedIPs := normalizedStringList(ctx, plan.AllowedIPs, diags)
	if allowedIPs.IsNull() {
		diags.AddError("Invalid allowed_ips", "allowed_ips must include at least one value.")
	}
	endpointPort := types.Int64Null()
	if !plan.EndpointPort.IsNull() && !plan.EndpointPort.IsUnknown() {
		port := plan.EndpointPort.ValueInt64()
		if port <= 0 || port > 65535 {
			diags.AddError("Invalid endpoint_port", "endpoint_port must be between 1 and 65535.")
		} else {
			endpointPort = types.Int64Value(port)
		}
	}
	persistentKeepalive := types.Int64Null()
	if !plan.PersistentKeepalive.IsNull() && !plan.PersistentKeepalive.IsUnknown() {
		seconds := plan.PersistentKeepalive.ValueInt64()
		if seconds < 0 || seconds > 65535 {
			diags.AddError("Invalid persistent_keepalive", "persistent_keepalive must be between 0 and 65535.")
		} else {
			persistentKeepalive = types.Int64Value(seconds)
		}
	}
	if diags.HasError() {
		return ResourceModel{}, false
	}
	return ResourceModel{
		ID:                  types.StringValue(name),
		Name:                types.StringValue(name),
		Interface:           types.StringValue(iface),
		PublicKey:           types.StringValue(publicKey),
		AllowedIPs:          allowedIPs,
		PresharedKey:        normalizeOptional(plan.PresharedKey),
		EndpointHost:        normalizeOptional(plan.EndpointHost),
		EndpointPort:        endpointPort,
		PersistentKeepalive: persistentKeepalive,
		Description:         normalizeOptional(plan.Description),
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

func canonicalInterface(value string) (string, error) {
	canonical := strings.ToLower(strings.TrimSpace(value))
	if canonical == "" {
		return "", fmt.Errorf("interface cannot be empty")
	}
	if !validInterfaceChar.MatchString(canonical) {
		return "", fmt.Errorf("interface must match %s", validInterfaceChar.String())
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

func normalizedStringList(ctx context.Context, raw types.List, diags *diag.Diagnostics) types.List {
	if raw.IsNull() || raw.IsUnknown() {
		return types.ListNull(types.StringType)
	}
	var values []string
	diags.Append(raw.ElementsAs(ctx, &values, false)...)
	if diags.HasError() {
		return types.ListNull(types.StringType)
	}
	normalized := normalizeStringSlice(values)
	if len(normalized) == 0 {
		return types.ListNull(types.StringType)
	}
	out, diagnostics := types.ListValueFrom(ctx, types.StringType, normalized)
	diags.Append(diagnostics...)
	if diags.HasError() {
		return types.ListNull(types.StringType)
	}
	return out
}

func normalizeStringSlice(values []string) []string {
	seen := map[string]bool{}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		normalized = append(normalized, trimmed)
	}
	slices.Sort(normalized)
	return normalized
}

func allowedIPsFromValues(ctx context.Context, values map[string]modernubus.UCIValue) (types.List, error) {
	raw, ok := values["allowed_ips"]
	if !ok {
		return types.ListNull(types.StringType), nil
	}
	if typed, ok := raw.Raw().([]string); ok {
		normalized := normalizeStringSlice(typed)
		if len(normalized) == 0 {
			return types.ListNull(types.StringType), nil
		}
		out, diagnostics := types.ListValueFrom(ctx, types.StringType, normalized)
		if diagnostics.HasError() {
			return types.ListNull(types.StringType), fmt.Errorf("invalid allowed_ips list value")
		}
		return out, nil
	}
	if list, ok := raw.List(); ok {
		normalized := normalizeStringSlice(list)
		if len(normalized) == 0 {
			return types.ListNull(types.StringType), nil
		}
		out, diagnostics := types.ListValueFrom(ctx, types.StringType, normalized)
		if diagnostics.HasError() {
			return types.ListNull(types.StringType), fmt.Errorf("invalid allowed_ips list value")
		}
		return out, nil
	}
	if text, ok := raw.String(); ok {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return types.ListNull(types.StringType), nil
		}
		out, diagnostics := types.ListValueFrom(ctx, types.StringType, []string{trimmed})
		if diagnostics.HasError() {
			return types.ListNull(types.StringType), fmt.Errorf("invalid allowed_ips list value")
		}
		return out, nil
	}
	return types.ListNull(types.StringType), fmt.Errorf("WireGuard peer allowed_ips option has unsupported type %T", raw.Raw())
}

func peerSectionType(iface string) string {
	return "wireguard_" + iface
}

func sectionNameForPeer(iface, name string) string {
	canonicalNameValue, err := canonicalName(name)
	if err != nil {
		canonicalNameValue = "invalid"
	}
	readable := strings.ReplaceAll(canonicalNameValue, ".", "_")
	readable = strings.ReplaceAll(readable, "-", "_")
	readable = nonSectionChar.ReplaceAllString(readable, "_")
	readable = strings.Trim(readable, "_")
	if readable == "" {
		readable = "peer"
	}
	if len(readable) > sectionReadableMax {
		readable = readable[:sectionReadableMax]
	}
	hash := sha256.Sum256([]byte(iface + "/" + canonicalNameValue))
	hashHex := hex.EncodeToString(hash[:])
	return sectionPrefix + readable + "_" + hashHex[:sectionHashHexLen]
}

func optionalOptionsToDelete(existing modernubus.UCIGetResponse, desired ResourceModel) []string {
	optional := []struct {
		field  types.String
		option string
	}{
		{desired.PresharedKey, "preshared_key"},
		{desired.EndpointHost, "endpoint_host"},
		{desired.Description, "description"},
	}
	out := make([]string, 0, len(optional)+2)
	for _, item := range optional {
		if item.field.IsNull() {
			if _, ok := existing.Values[item.option]; ok {
				out = append(out, item.option)
			}
		}
	}
	if desired.EndpointPort.IsNull() {
		if _, ok := existing.Values["endpoint_port"]; ok {
			out = append(out, "endpoint_port")
		}
	}
	if desired.PersistentKeepalive.IsNull() {
		if _, ok := existing.Values["persistent_keepalive"]; ok {
			out = append(out, "persistent_keepalive")
		}
	}
	slices.Sort(out)
	return out
}

func peerValues(model ResourceModel) map[string]any {
	values := map[string]any{
		"public_key":  model.PublicKey.ValueString(),
		"allowed_ips": listValues(model.AllowedIPs),
	}
	if !model.PresharedKey.IsNull() && !model.PresharedKey.IsUnknown() {
		values["preshared_key"] = model.PresharedKey.ValueString()
	}
	if !model.EndpointHost.IsNull() && !model.EndpointHost.IsUnknown() {
		values["endpoint_host"] = model.EndpointHost.ValueString()
	}
	if !model.EndpointPort.IsNull() && !model.EndpointPort.IsUnknown() {
		values["endpoint_port"] = strconv.FormatInt(model.EndpointPort.ValueInt64(), 10)
	}
	if !model.PersistentKeepalive.IsNull() && !model.PersistentKeepalive.IsUnknown() {
		values["persistent_keepalive"] = strconv.FormatInt(model.PersistentKeepalive.ValueInt64(), 10)
	}
	if !model.Description.IsNull() && !model.Description.IsUnknown() {
		values["description"] = model.Description.ValueString()
	}
	return values
}

func listValues(value types.List) []string {
	if value.IsNull() || value.IsUnknown() {
		return nil
	}
	var out []string
	_ = value.ElementsAs(context.Background(), &out, false)
	return out
}

func modelsEqual(left, right ResourceModel) bool {
	return left.ID.Equal(right.ID) &&
		left.Name.Equal(right.Name) &&
		left.Interface.Equal(right.Interface) &&
		left.PublicKey.Equal(right.PublicKey) &&
		left.AllowedIPs.Equal(right.AllowedIPs) &&
		left.PresharedKey.Equal(right.PresharedKey) &&
		left.EndpointHost.Equal(right.EndpointHost) &&
		left.EndpointPort.Equal(right.EndpointPort) &&
		left.PersistentKeepalive.Equal(right.PersistentKeepalive) &&
		left.Description.Equal(right.Description)
}

func transactionLifetime(applyTimeoutSec int64) time.Duration {
	return time.Duration(applyTimeoutSec+5) * time.Second
}

func verifyRouterHealth(ctx context.Context, client ubusWireGuardPeerClient) error {
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
