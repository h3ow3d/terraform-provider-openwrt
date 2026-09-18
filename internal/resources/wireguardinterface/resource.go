package wireguardinterface

import (
	"context"
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
	interfaceSectionType   = "interface"
	defaultApplyTimeoutSec = int64(10)
	maxNameLength          = 63
)

var sectionNamePattern = regexp.MustCompile(`^[a-z0-9_]+$`)

type ubusWGInterfaceClient interface {
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
	client ubusWGInterfaceClient
}

type ResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	PrivateKey types.String `tfsdk:"private_key"`
	ListenPort types.Int64  `tfsdk:"listen_port"`
	Addresses  types.List   `tfsdk:"addresses"`
}

func NewResource() resource.Resource { return &Resource{} }

func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_wireguard_interface"
}

func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "WireGuard interface section in /etc/config/network.",
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
			"private_key": schema.StringAttribute{Required: true, Sensitive: true},
			"listen_port": schema.Int64Attribute{Optional: true},
			"addresses": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
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
	desired, ok := normalizePlan(ctx, plan, &resp.Diagnostics)
	if !ok {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}
	if err := createInterface(ctx, r.client, desired, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to create WireGuard interface", err.Error())
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
	live, exists, err := readLiveInterface(ctx, r.client, name)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read WireGuard interface", err.Error())
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
	if err := updateInterface(ctx, r.client, desired, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to update WireGuard interface", err.Error())
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
	if err := deleteInterface(ctx, r.client, name, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to delete WireGuard interface", err.Error())
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

func createInterface(ctx context.Context, client ubusWGInterfaceClient, desired ResourceModel, applyTimeoutSec int64) error {
	name := desired.Name.ValueString()
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: networkPackage, Section: name})
		if err != nil {
			return err
		}
		if existing.SectionExists {
			return fmt.Errorf("section for WireGuard interface already exists; import this resource using identifier %q", name)
		}
		added, err := client.UCIAdd(txCtx, modernubus.UCIAddRequest{Config: networkPackage, Type: interfaceSectionType, Name: name, Values: interfaceValues(desired)})
		if err != nil {
			return err
		}
		if added.Section != name {
			return fmt.Errorf("uci.add created unexpected section name")
		}
		return applyVerifyConfirm(txCtx, ctx, client, desired, applyTimeoutSec)
	})
}

func updateInterface(ctx context.Context, client ubusWGInterfaceClient, desired ResourceModel, applyTimeoutSec int64) error {
	section := desired.Name.ValueString()
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: networkPackage, Section: section})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return fmt.Errorf("managed section for WireGuard interface is missing; run import or recreate")
		}
		for _, option := range optionalOptionsToDelete(existing, desired) {
			if _, err := client.UCIDelete(txCtx, modernubus.UCIDeleteRequest{Config: networkPackage, Section: section, Option: option}); err != nil {
				return err
			}
		}
		if _, err := client.UCISet(txCtx, modernubus.UCISetRequest{Config: networkPackage, Section: section, Values: interfaceValues(desired)}); err != nil {
			return err
		}
		return applyVerifyConfirm(txCtx, ctx, client, desired, applyTimeoutSec)
	})
}

func deleteInterface(ctx context.Context, client ubusWGInterfaceClient, name string, applyTimeoutSec int64) error {
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: networkPackage, Section: name})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return nil
		}
		if _, err := client.UCIDelete(txCtx, modernubus.UCIDeleteRequest{Config: networkPackage, Section: name}); err != nil {
			return err
		}
		if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{Rollback: true, Timeout: applyTimeoutSec}); err != nil {
			return err
		}
		if err := verifyRouterHealth(ctx, client); err != nil {
			return err
		}
		readBack, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: networkPackage, Section: name})
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

func applyVerifyConfirm(txCtx, healthCtx context.Context, client ubusWGInterfaceClient, expected ResourceModel, applyTimeoutSec int64) error {
	if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{Rollback: true, Timeout: applyTimeoutSec}); err != nil {
		return err
	}
	if err := verifyRouterHealth(healthCtx, client); err != nil {
		return err
	}
	live, exists, err := readLiveInterface(txCtx, client, expected.Name.ValueString())
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

func readLiveInterface(ctx context.Context, client ubusWGInterfaceClient, name string) (ResourceModel, bool, error) {
	resp, err := client.UCIGet(ctx, modernubus.UCIGetRequest{Config: networkPackage, Section: name})
	if err != nil {
		return ResourceModel{}, false, err
	}
	if !resp.SectionExists {
		return ResourceModel{}, false, nil
	}
	proto, _ := resp.Values["proto"].String()
	if strings.TrimSpace(proto) != "wireguard" {
		return ResourceModel{}, false, fmt.Errorf("section exists but is not a WireGuard interface")
	}
	privateKey, ok := resp.Values["private_key"].String()
	if !ok || strings.TrimSpace(privateKey) == "" {
		return ResourceModel{}, false, fmt.Errorf("WireGuard interface section is missing required private_key option")
	}
	listenPort := types.Int64Null()
	if raw, exists := resp.Values["listen_port"]; exists {
		if text, ok := raw.String(); ok {
			parsed, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
			if err == nil && parsed > 0 {
				listenPort = types.Int64Value(parsed)
			}
		}
	}
	addresses, err := addressesFromValues(ctx, resp.Values)
	if err != nil {
		return ResourceModel{}, false, err
	}
	return ResourceModel{
		ID:         types.StringValue(name),
		Name:       types.StringValue(name),
		PrivateKey: types.StringValue(strings.TrimSpace(privateKey)),
		ListenPort: listenPort,
		Addresses:  addresses,
	}, true, nil
}

func normalizePlan(ctx context.Context, plan ResourceModel, diags *diag.Diagnostics) (ResourceModel, bool) {
	name, err := canonicalName(plan.Name.ValueString())
	if err != nil {
		diags.AddError("Invalid WireGuard interface name", err.Error())
	}
	privateKey := strings.TrimSpace(plan.PrivateKey.ValueString())
	if privateKey == "" {
		diags.AddError("Invalid private_key", "private_key cannot be empty.")
	}
	listenPort := types.Int64Null()
	if !plan.ListenPort.IsNull() && !plan.ListenPort.IsUnknown() {
		port := plan.ListenPort.ValueInt64()
		if port <= 0 || port > 65535 {
			diags.AddError("Invalid listen_port", "listen_port must be between 1 and 65535.")
		} else {
			listenPort = types.Int64Value(port)
		}
	}
	addresses := normalizedAddresses(ctx, plan.Addresses, diags)
	if diags.HasError() {
		return ResourceModel{}, false
	}
	return ResourceModel{
		ID:         types.StringValue(name),
		Name:       types.StringValue(name),
		PrivateKey: types.StringValue(privateKey),
		ListenPort: listenPort,
		Addresses:  addresses,
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
	if !sectionNamePattern.MatchString(canonical) {
		return "", fmt.Errorf("name must match %s", sectionNamePattern.String())
	}
	return canonical, nil
}

func normalizedAddresses(ctx context.Context, raw types.List, diags *diag.Diagnostics) types.List {
	if raw.IsNull() || raw.IsUnknown() {
		return types.ListNull(types.StringType)
	}
	var addresses []string
	diags.Append(raw.ElementsAs(ctx, &addresses, false)...)
	if diags.HasError() {
		return types.ListNull(types.StringType)
	}
	normalized := normalizeStringSlice(addresses)
	if len(normalized) == 0 {
		return types.ListNull(types.StringType)
	}
	result, diagnostics := types.ListValueFrom(ctx, types.StringType, normalized)
	diags.Append(diagnostics...)
	if diags.HasError() {
		return types.ListNull(types.StringType)
	}
	return result
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

func addressesFromValues(ctx context.Context, values map[string]modernubus.UCIValue) (types.List, error) {
	raw, ok := values["addresses"]
	if !ok {
		return types.ListNull(types.StringType), nil
	}
	if list, ok := raw.List(); ok {
		normalized := normalizeStringSlice(list)
		if len(normalized) == 0 {
			return types.ListNull(types.StringType), nil
		}
		out, diagnostics := types.ListValueFrom(ctx, types.StringType, normalized)
		if diagnostics.HasError() {
			return types.ListNull(types.StringType), fmt.Errorf("invalid addresses list value")
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
			return types.ListNull(types.StringType), fmt.Errorf("invalid addresses list value")
		}
		return out, nil
	}
	return types.ListNull(types.StringType), fmt.Errorf("WireGuard interface addresses option has unsupported type %T", raw.Raw())
}

func optionalOptionsToDelete(existing modernubus.UCIGetResponse, desired ResourceModel) []string {
	options := []string{}
	if desired.ListenPort.IsNull() {
		if _, ok := existing.Values["listen_port"]; ok {
			options = append(options, "listen_port")
		}
	}
	if desired.Addresses.IsNull() {
		if _, ok := existing.Values["addresses"]; ok {
			options = append(options, "addresses")
		}
	}
	return options
}

func interfaceValues(model ResourceModel) map[string]any {
	values := map[string]any{
		"proto":       "wireguard",
		"private_key": model.PrivateKey.ValueString(),
	}
	if !model.ListenPort.IsNull() && !model.ListenPort.IsUnknown() {
		values["listen_port"] = strconv.FormatInt(model.ListenPort.ValueInt64(), 10)
	}
	if !model.Addresses.IsNull() && !model.Addresses.IsUnknown() {
		var addresses []string
		_ = model.Addresses.ElementsAs(context.Background(), &addresses, false)
		if len(addresses) > 0 {
			values["addresses"] = addresses
		}
	}
	return values
}

func modelsEqual(left, right ResourceModel) bool {
	return left.ID.Equal(right.ID) &&
		left.Name.Equal(right.Name) &&
		left.PrivateKey.Equal(right.PrivateKey) &&
		left.ListenPort.Equal(right.ListenPort) &&
		left.Addresses.Equal(right.Addresses)
}

func transactionLifetime(applyTimeoutSec int64) time.Duration {
	return time.Duration(applyTimeoutSec+5) * time.Second
}

func verifyRouterHealth(ctx context.Context, client ubusWGInterfaceClient) error {
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
