package networkinterface

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
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

type ubusInterfaceClient interface {
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
	client ubusInterfaceClient
}

type ResourceModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Device    types.String `tfsdk:"device"`
	Proto     types.String `tfsdk:"proto"`
	CIDR      types.String `tfsdk:"cidr"`
	Gateway   types.String `tfsdk:"gateway"`
	MTU       types.Int64  `tfsdk:"mtu"`
	Delegate  types.Bool   `tfsdk:"delegate"`
	IP6Assign types.Int64  `tfsdk:"ip6assign"`
	DNS       types.List   `tfsdk:"dns"`
}

func NewResource() resource.Resource { return &Resource{} }

func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_interface"
}

func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "OpenWrt network interface section in /etc/config/network.",
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
			"device":    schema.StringAttribute{Required: true},
			"proto":     schema.StringAttribute{Optional: true, Computed: true},
			"cidr":      schema.StringAttribute{Optional: true},
			"gateway":   schema.StringAttribute{Optional: true},
			"mtu":       schema.Int64Attribute{Optional: true},
			"delegate":  schema.BoolAttribute{Optional: true, Computed: true},
			"ip6assign": schema.Int64Attribute{Optional: true},
			"dns": schema.ListAttribute{
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
		resp.Diagnostics.AddError("Failed to create interface", err.Error())
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
		resp.Diagnostics.AddError("Failed to read interface", err.Error())
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
		resp.Diagnostics.AddError("Failed to update interface", err.Error())
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
		resp.Diagnostics.AddError("Failed to delete interface", err.Error())
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

func createInterface(ctx context.Context, client ubusInterfaceClient, desired ResourceModel, applyTimeoutSec int64) error {
	name := desired.Name.ValueString()
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: networkPackage, Section: name})
		if err != nil {
			return err
		}
		if existing.SectionExists {
			return fmt.Errorf("section for interface already exists; import this resource using identifier %q", name)
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

func updateInterface(ctx context.Context, client ubusInterfaceClient, desired ResourceModel, applyTimeoutSec int64) error {
	section := desired.Name.ValueString()
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: networkPackage, Section: section})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return fmt.Errorf("managed section for interface is missing; run import or recreate")
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

func deleteInterface(ctx context.Context, client ubusInterfaceClient, name string, applyTimeoutSec int64) error {
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

func applyVerifyConfirm(txCtx, healthCtx context.Context, client ubusInterfaceClient, expected ResourceModel, applyTimeoutSec int64) error {
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

func readLiveInterface(ctx context.Context, client ubusInterfaceClient, name string) (ResourceModel, bool, error) {
	resp, err := client.UCIGet(ctx, modernubus.UCIGetRequest{Config: networkPackage, Section: name})
	if err != nil {
		return ResourceModel{}, false, err
	}
	if !resp.SectionExists {
		return ResourceModel{}, false, nil
	}
	device, ok := resp.Values["device"].String()
	if !ok || strings.TrimSpace(device) == "" {
		return ResourceModel{}, false, fmt.Errorf("interface section is missing required device option")
	}
	proto := "static"
	if raw, has := resp.Values["proto"]; has {
		if parsed, ok := raw.String(); ok && strings.TrimSpace(parsed) != "" {
			proto = strings.ToLower(strings.TrimSpace(parsed))
		}
	}
	cidr, err := cidrFromValues(resp.Values)
	if err != nil {
		return ResourceModel{}, false, err
	}
	gateway := types.StringNull()
	if raw, exists := resp.Values["gateway"]; exists {
		if value, ok := raw.String(); ok && strings.TrimSpace(value) != "" {
			gateway = types.StringValue(strings.TrimSpace(value))
		}
	}
	mtu := types.Int64Null()
	if raw, exists := resp.Values["mtu"]; exists {
		if value, ok := raw.String(); ok {
			parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err == nil && parsed >= 576 && parsed <= 9200 {
				mtu = types.Int64Value(parsed)
			}
		}
	}
	delegate := types.BoolValue(false)
	if raw, exists := resp.Values["delegate"]; exists {
		if value, ok := raw.String(); ok {
			delegate = types.BoolValue(parseUCIBool(value))
		}
	}
	ip6assign := types.Int64Null()
	if raw, exists := resp.Values["ip6assign"]; exists {
		if value, ok := raw.String(); ok {
			parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err == nil && parsed >= 0 && parsed <= 128 {
				ip6assign = types.Int64Value(parsed)
			}
		}
	}
	dns, err := dnsFromValues(ctx, resp.Values)
	if err != nil {
		return ResourceModel{}, false, err
	}
	return ResourceModel{
		ID:        types.StringValue(name),
		Name:      types.StringValue(name),
		Device:    types.StringValue(strings.TrimSpace(device)),
		Proto:     types.StringValue(proto),
		CIDR:      cidr,
		Gateway:   gateway,
		MTU:       mtu,
		Delegate:  delegate,
		IP6Assign: ip6assign,
		DNS:       dns,
	}, true, nil
}

func normalizePlan(ctx context.Context, plan ResourceModel, diags *diag.Diagnostics) (ResourceModel, bool) {
	name, err := canonicalName(plan.Name.ValueString())
	if err != nil {
		diags.AddError("Invalid interface name", err.Error())
	}
	device := strings.TrimSpace(plan.Device.ValueString())
	if device == "" {
		diags.AddError("Invalid device", "device cannot be empty.")
	}
	proto := "static"
	if !plan.Proto.IsNull() && !plan.Proto.IsUnknown() {
		proto = strings.ToLower(strings.TrimSpace(plan.Proto.ValueString()))
	}
	if proto == "" {
		diags.AddError("Invalid proto", "proto cannot be empty.")
	}
	cidr := types.StringNull()
	if !plan.CIDR.IsNull() && !plan.CIDR.IsUnknown() {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(plan.CIDR.ValueString()))
		if err != nil {
			diags.AddError("Invalid cidr", "cidr must be valid IPv4/IPv6 CIDR notation.")
		} else {
			cidr = types.StringValue(prefix.String())
		}
	}
	if proto == "static" && cidr.IsNull() {
		diags.AddError("Invalid cidr", "cidr is required when proto is static.")
	}
	gateway := types.StringNull()
	if !plan.Gateway.IsNull() && !plan.Gateway.IsUnknown() {
		value := strings.TrimSpace(plan.Gateway.ValueString())
		if value == "" {
			diags.AddError("Invalid gateway", "gateway cannot be empty when set.")
		} else if ip := net.ParseIP(value); ip == nil {
			diags.AddError("Invalid gateway", "gateway must be a valid IPv4/IPv6 address.")
		} else {
			gateway = types.StringValue(value)
		}
	}
	mtu := types.Int64Null()
	if !plan.MTU.IsNull() && !plan.MTU.IsUnknown() {
		value := plan.MTU.ValueInt64()
		if value < 576 || value > 9200 {
			diags.AddError("Invalid mtu", "mtu must be between 576 and 9200.")
		} else {
			mtu = types.Int64Value(value)
		}
	}
	delegate := types.BoolValue(false)
	if !plan.Delegate.IsNull() && !plan.Delegate.IsUnknown() {
		delegate = types.BoolValue(plan.Delegate.ValueBool())
	}
	ip6assign := types.Int64Null()
	if !plan.IP6Assign.IsNull() && !plan.IP6Assign.IsUnknown() {
		value := plan.IP6Assign.ValueInt64()
		if value < 0 || value > 128 {
			diags.AddError("Invalid ip6assign", "ip6assign must be between 0 and 128.")
		} else {
			ip6assign = types.Int64Value(value)
		}
	}
	dns := normalizedStringList(ctx, plan.DNS, diags)
	if diags.HasError() {
		return ResourceModel{}, false
	}
	return ResourceModel{
		ID:        types.StringValue(name),
		Name:      types.StringValue(name),
		Device:    types.StringValue(device),
		Proto:     types.StringValue(proto),
		CIDR:      cidr,
		Gateway:   gateway,
		MTU:       mtu,
		Delegate:  delegate,
		IP6Assign: ip6assign,
		DNS:       dns,
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

func cidrToAddressMask(value string) (string, string, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil {
		return "", "", err
	}
	if !prefix.Addr().Is4() {
		return "", "", fmt.Errorf("only IPv4 cidr is supported for now")
	}
	bits := prefix.Bits()
	mask := net.CIDRMask(bits, 32)
	return prefix.Addr().String(), net.IP(mask).String(), nil
}

func cidrFromValues(values map[string]modernubus.UCIValue) (types.String, error) {
	rawIP, hasIP := values["ipaddr"]
	rawMask, hasMask := values["netmask"]
	if !hasIP || !hasMask {
		return types.StringNull(), nil
	}
	ipText, okIP := rawIP.String()
	maskText, okMask := rawMask.String()
	if !okIP || !okMask {
		return types.StringNull(), fmt.Errorf("interface ipaddr/netmask options have unsupported values")
	}
	ip := net.ParseIP(strings.TrimSpace(ipText))
	maskIP := net.ParseIP(strings.TrimSpace(maskText))
	if ip == nil || maskIP == nil {
		return types.StringNull(), fmt.Errorf("interface ipaddr/netmask options are invalid")
	}
	ipv4 := ip.To4()
	mask4 := maskIP.To4()
	if ipv4 == nil || mask4 == nil {
		return types.StringNull(), fmt.Errorf("only IPv4 ipaddr/netmask combinations are supported")
	}
	prefixLen, bits := net.IPMask(mask4).Size()
	if bits != 32 {
		return types.StringNull(), fmt.Errorf("invalid interface netmask")
	}
	prefix := netip.PrefixFrom(netip.AddrFrom4([4]byte{ipv4[0], ipv4[1], ipv4[2], ipv4[3]}), prefixLen)
	return types.StringValue(prefix.String()), nil
}

func dnsFromValues(ctx context.Context, values map[string]modernubus.UCIValue) (types.List, error) {
	raw, ok := values["dns"]
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
			return types.ListNull(types.StringType), fmt.Errorf("invalid dns list value")
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
			return types.ListNull(types.StringType), fmt.Errorf("invalid dns list value")
		}
		return out, nil
	}
	return types.ListNull(types.StringType), fmt.Errorf("interface dns option has unsupported type %T", raw.Raw())
}

func parseUCIBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}

func optionalOptionsToDelete(existing modernubus.UCIGetResponse, desired ResourceModel) []string {
	options := []string{}
	if desired.CIDR.IsNull() {
		if _, ok := existing.Values["ipaddr"]; ok {
			options = append(options, "ipaddr")
		}
		if _, ok := existing.Values["netmask"]; ok {
			options = append(options, "netmask")
		}
	}
	if desired.Gateway.IsNull() {
		if _, ok := existing.Values["gateway"]; ok {
			options = append(options, "gateway")
		}
	}
	if desired.MTU.IsNull() {
		if _, ok := existing.Values["mtu"]; ok {
			options = append(options, "mtu")
		}
	}
	if desired.IP6Assign.IsNull() {
		if _, ok := existing.Values["ip6assign"]; ok {
			options = append(options, "ip6assign")
		}
	}
	if desired.DNS.IsNull() {
		if _, ok := existing.Values["dns"]; ok {
			options = append(options, "dns")
		}
	}
	return options
}

func interfaceValues(model ResourceModel) map[string]any {
	values := map[string]any{
		"device":   model.Device.ValueString(),
		"proto":    model.Proto.ValueString(),
		"delegate": boolToUCIValue(model.Delegate.ValueBool()),
	}
	if !model.CIDR.IsNull() && !model.CIDR.IsUnknown() {
		ipaddr, netmask, err := cidrToAddressMask(model.CIDR.ValueString())
		if err == nil {
			values["ipaddr"] = ipaddr
			values["netmask"] = netmask
		}
	}
	if !model.Gateway.IsNull() && !model.Gateway.IsUnknown() {
		values["gateway"] = model.Gateway.ValueString()
	}
	if !model.MTU.IsNull() && !model.MTU.IsUnknown() {
		values["mtu"] = strconv.FormatInt(model.MTU.ValueInt64(), 10)
	}
	if !model.IP6Assign.IsNull() && !model.IP6Assign.IsUnknown() {
		values["ip6assign"] = strconv.FormatInt(model.IP6Assign.ValueInt64(), 10)
	}
	if !model.DNS.IsNull() && !model.DNS.IsUnknown() {
		var dns []string
		_ = model.DNS.ElementsAs(context.Background(), &dns, false)
		if len(dns) > 0 {
			values["dns"] = dns
		}
	}
	return values
}

func boolToUCIValue(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func modelsEqual(left, right ResourceModel) bool {
	return left.ID.Equal(right.ID) &&
		left.Name.Equal(right.Name) &&
		left.Device.Equal(right.Device) &&
		left.Proto.Equal(right.Proto) &&
		left.CIDR.Equal(right.CIDR) &&
		left.Gateway.Equal(right.Gateway) &&
		left.MTU.Equal(right.MTU) &&
		left.Delegate.Equal(right.Delegate) &&
		left.IP6Assign.Equal(right.IP6Assign) &&
		left.DNS.Equal(right.DNS)
}

func transactionLifetime(applyTimeoutSec int64) time.Duration {
	return time.Duration(applyTimeoutSec+5) * time.Second
}

func verifyRouterHealth(ctx context.Context, client ubusInterfaceClient) error {
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
