package device

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
	networkPackage         = "network"
	deviceSectionType      = "device"
	defaultApplyTimeoutSec = int64(10)
	sectionPrefix          = "tfdev_"
	sectionReadableMax     = 24
	sectionHashHexLen      = 16
	maxNameLength          = 63
)

var validNameChar = regexp.MustCompile(`^[a-z0-9._-]+$`)
var nonSectionChar = regexp.MustCompile(`[^a-z0-9_]+`)

type ubusDeviceClient interface {
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
	client ubusDeviceClient
}

type ResourceModel struct {
	ID    types.String `tfsdk:"id"`
	Name  types.String `tfsdk:"name"`
	Type  types.String `tfsdk:"type"`
	Ports types.List   `tfsdk:"ports"`
}

func NewResource() resource.Resource { return &Resource{} }

func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_device"
}

func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "OpenWrt network device section in /etc/config/network.",
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
			"type": schema.StringAttribute{Required: true},
			"ports": schema.ListAttribute{
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
	if err := createDevice(ctx, r.client, desired, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to create device", err.Error())
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
	live, exists, err := readLiveDevice(ctx, r.client, name)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read device", err.Error())
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
	if err := updateDevice(ctx, r.client, desired, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to update device", err.Error())
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
	if err := deleteDevice(ctx, r.client, name, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to delete device", err.Error())
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

func createDevice(ctx context.Context, client ubusDeviceClient, desired ResourceModel, applyTimeoutSec int64) error {
	name := desired.Name.ValueString()
	section := sectionNameForDevice(name)
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: networkPackage, Section: section})
		if err != nil {
			return err
		}
		if existing.SectionExists {
			return fmt.Errorf("section for device already exists; import this resource using identifier %q", name)
		}
		added, err := client.UCIAdd(txCtx, modernubus.UCIAddRequest{
			Config: networkPackage,
			Type:   deviceSectionType,
			Name:   section,
			Values: deviceValues(desired),
		})
		if err != nil {
			return err
		}
		if added.Section != section {
			return fmt.Errorf("uci.add created unexpected section name")
		}
		return applyVerifyConfirm(txCtx, ctx, client, desired, applyTimeoutSec)
	})
}

func updateDevice(ctx context.Context, client ubusDeviceClient, desired ResourceModel, applyTimeoutSec int64) error {
	section := sectionNameForDevice(desired.Name.ValueString())
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: networkPackage, Section: section})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return fmt.Errorf("managed section for device is missing; run import or recreate")
		}
		if shouldDeletePorts(existing, desired) {
			if _, err := client.UCIDelete(txCtx, modernubus.UCIDeleteRequest{Config: networkPackage, Section: section, Option: "ports"}); err != nil {
				return err
			}
		}
		if _, err := client.UCISet(txCtx, modernubus.UCISetRequest{Config: networkPackage, Section: section, Values: deviceValues(desired)}); err != nil {
			return err
		}
		return applyVerifyConfirm(txCtx, ctx, client, desired, applyTimeoutSec)
	})
}

func deleteDevice(ctx context.Context, client ubusDeviceClient, name string, applyTimeoutSec int64) error {
	section := sectionNameForDevice(name)
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

func applyVerifyConfirm(txCtx, healthCtx context.Context, client ubusDeviceClient, expected ResourceModel, applyTimeoutSec int64) error {
	if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{Rollback: true, Timeout: applyTimeoutSec}); err != nil {
		return err
	}
	if err := verifyRouterHealth(healthCtx, client); err != nil {
		return err
	}
	live, exists, err := readLiveDevice(txCtx, client, expected.Name.ValueString())
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

func normalizePlan(ctx context.Context, plan ResourceModel, diags *diag.Diagnostics) (ResourceModel, bool) {
	name, err := canonicalName(plan.Name.ValueString())
	if err != nil {
		diags.AddError("Invalid device name", err.Error())
	}
	deviceType := strings.TrimSpace(plan.Type.ValueString())
	if deviceType == "" {
		diags.AddError("Invalid device type", "type cannot be empty.")
	}
	ports := normalizedPorts(ctx, plan.Ports, diags)
	if diags.HasError() {
		return ResourceModel{}, false
	}

	return ResourceModel{
		ID:    types.StringValue(name),
		Name:  types.StringValue(name),
		Type:  types.StringValue(deviceType),
		Ports: ports,
	}, true
}

func normalizedPorts(ctx context.Context, raw types.List, diags *diag.Diagnostics) types.List {
	if raw.IsNull() || raw.IsUnknown() {
		return types.ListNull(types.StringType)
	}
	var ports []string
	diags.Append(raw.ElementsAs(ctx, &ports, false)...)
	if diags.HasError() {
		return types.ListNull(types.StringType)
	}
	seen := map[string]bool{}
	normalized := make([]string, 0, len(ports))
	for _, port := range ports {
		trimmed := strings.TrimSpace(port)
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

func sectionNameForDevice(name string) string {
	canonical, err := canonicalName(name)
	if err != nil {
		canonical = "invalid"
	}
	readable := strings.ReplaceAll(canonical, ".", "_")
	readable = strings.ReplaceAll(readable, "-", "_")
	readable = nonSectionChar.ReplaceAllString(readable, "_")
	readable = strings.Trim(readable, "_")
	if readable == "" {
		readable = "device"
	}
	if len(readable) > sectionReadableMax {
		readable = readable[:sectionReadableMax]
	}
	hash := sha256.Sum256([]byte(canonical))
	return sectionPrefix + readable + "_" + hex.EncodeToString(hash[:])[:sectionHashHexLen]
}

func readLiveDevice(ctx context.Context, client ubusDeviceClient, name string) (ResourceModel, bool, error) {
	resp, err := client.UCIGet(ctx, modernubus.UCIGetRequest{Config: networkPackage, Section: sectionNameForDevice(name)})
	if err != nil {
		return ResourceModel{}, false, err
	}
	if !resp.SectionExists {
		return ResourceModel{}, false, nil
	}
	deviceType, ok := resp.Values["type"].String()
	if !ok || strings.TrimSpace(deviceType) == "" {
		return ResourceModel{}, false, fmt.Errorf("device section is missing required type option")
	}
	deviceName := name
	if rawName, ok := resp.Values["name"]; ok {
		if parsed, ok := rawName.String(); ok && strings.TrimSpace(parsed) != "" {
			deviceName = strings.TrimSpace(parsed)
		}
	}
	canonicalNameValue, err := canonicalName(deviceName)
	if err != nil {
		return ResourceModel{}, false, err
	}
	ports, err := portsFromValues(ctx, resp.Values)
	if err != nil {
		return ResourceModel{}, false, err
	}
	return ResourceModel{
		ID:    types.StringValue(canonicalNameValue),
		Name:  types.StringValue(canonicalNameValue),
		Type:  types.StringValue(strings.TrimSpace(deviceType)),
		Ports: ports,
	}, true, nil
}

func portsFromValues(ctx context.Context, values map[string]modernubus.UCIValue) (types.List, error) {
	rawPorts, ok := values["ports"]
	if !ok {
		return types.ListNull(types.StringType), nil
	}
	if list, ok := rawPorts.List(); ok {
		normalized := normalizeStringSlice(list)
		if len(normalized) == 0 {
			return types.ListNull(types.StringType), nil
		}
		out, diagnostics := types.ListValueFrom(ctx, types.StringType, normalized)
		if diagnostics.HasError() {
			return types.ListNull(types.StringType), fmt.Errorf("invalid ports list value")
		}
		return out, nil
	}
	switch typed := rawPorts.Raw().(type) {
	case []string:
		normalized := normalizeStringSlice(typed)
		if len(normalized) == 0 {
			return types.ListNull(types.StringType), nil
		}
		out, diagnostics := types.ListValueFrom(ctx, types.StringType, normalized)
		if diagnostics.HasError() {
			return types.ListNull(types.StringType), fmt.Errorf("invalid ports list value")
		}
		return out, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return types.ListNull(types.StringType), nil
		}
		out, diagnostics := types.ListValueFrom(ctx, types.StringType, []string{trimmed})
		if diagnostics.HasError() {
			return types.ListNull(types.StringType), fmt.Errorf("invalid ports list value")
		}
		return out, nil
	default:
		return types.ListNull(types.StringType), fmt.Errorf("device ports option has unsupported type %T", rawPorts.Raw())
	}
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

func shouldDeletePorts(existing modernubus.UCIGetResponse, desired ResourceModel) bool {
	if !desired.Ports.IsNull() {
		return false
	}
	_, exists := existing.Values["ports"]
	return exists
}

func deviceValues(model ResourceModel) map[string]any {
	values := map[string]any{
		"name": model.Name.ValueString(),
		"type": model.Type.ValueString(),
	}
	if !model.Ports.IsNull() && !model.Ports.IsUnknown() {
		var ports []string
		_ = model.Ports.ElementsAs(context.Background(), &ports, false)
		if len(ports) > 0 {
			values["ports"] = ports
		}
	}
	return values
}

func modelsEqual(left, right ResourceModel) bool {
	return left.ID.Equal(right.ID) &&
		left.Name.Equal(right.Name) &&
		left.Type.Equal(right.Type) &&
		left.Ports.Equal(right.Ports)
}

func transactionLifetime(applyTimeoutSec int64) time.Duration {
	return time.Duration(applyTimeoutSec+5) * time.Second
}

func verifyRouterHealth(ctx context.Context, client ubusDeviceClient) error {
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
