package segment

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/ophomelab/terraform-provider-openwrt/internal/client/luci"
)

var (
	_ resource.Resource                = (*Resource)(nil)
	_ resource.ResourceWithConfigure   = (*Resource)(nil)
	_ resource.ResourceWithImportState = (*Resource)(nil)
)

type Resource struct {
	client luci.Client
}

type ResourceModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Device          types.String `tfsdk:"device"`
	Proto           types.String `tfsdk:"proto"`
	CIDR            types.String `tfsdk:"cidr"`
	Zone            types.String `tfsdk:"zone"`
	DNS             types.List   `tfsdk:"dns"`
	Gateway         types.String `tfsdk:"gateway"`
	DHCPStart       types.Int64  `tfsdk:"dhcp_start"`
	DHCPLimit       types.Int64  `tfsdk:"dhcp_limit"`
	DHCPLeaseTime   types.String `tfsdk:"dhcp_leasetime"`
	AllowWanForward types.Bool   `tfsdk:"allow_wan_forward"`
	FirewallInput   types.String `tfsdk:"firewall_input"`
	FirewallOutput  types.String `tfsdk:"firewall_output"`
	FirewallForward types.String `tfsdk:"firewall_forward"`
	ParentInterface types.String `tfsdk:"parent_interface"`
	VLANID          types.Int64  `tfsdk:"vlan_id"`
}

func NewResource() resource.Resource { return &Resource{} }

func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_segment"
}

func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Comprehensive OpenWrt segment orchestration: network interface + DHCP scope + firewall zone + optional WAN forwarding.",
		Attributes: map[string]schema.Attribute{
			"id":                schema.StringAttribute{Computed: true},
			"name":              schema.StringAttribute{Required: true},
			"device":            schema.StringAttribute{Required: true},
			"proto":             schema.StringAttribute{Optional: true, Computed: true},
			"cidr":              schema.StringAttribute{Required: true},
			"zone":              schema.StringAttribute{Optional: true, Computed: true},
			"dns":               schema.ListAttribute{Optional: true, ElementType: types.StringType},
			"gateway":           schema.StringAttribute{Optional: true},
			"dhcp_start":        schema.Int64Attribute{Required: true},
			"dhcp_limit":        schema.Int64Attribute{Required: true},
			"dhcp_leasetime":    schema.StringAttribute{Optional: true, Computed: true},
			"allow_wan_forward": schema.BoolAttribute{Optional: true, Computed: true},
			"firewall_input":    schema.StringAttribute{Optional: true, Computed: true},
			"firewall_output":   schema.StringAttribute{Optional: true, Computed: true},
			"firewall_forward":  schema.StringAttribute{Optional: true, Computed: true},
			"parent_interface":  schema.StringAttribute{Optional: true},
			"vlan_id":           schema.Int64Attribute{Optional: true},
		},
	}
}

func (r *Resource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(luci.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected luci.Client, got %T", req.ProviderData))
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
	if !validatePlan(plan, &resp.Diagnostics) {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}

	if err := r.client.ApplyManagedBlocks(ctx, segmentBlocks(plan, false)); err != nil {
		resp.Diagnostics.AddError("Failed to apply segment", err.Error())
		return
	}

	plan.ID = types.StringValue(plan.Name.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
}

func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	applyDefaults(&plan)
	if !validatePlan(plan, &resp.Diagnostics) {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}

	if err := r.client.ApplyManagedBlocks(ctx, segmentBlocks(plan, false)); err != nil {
		resp.Diagnostics.AddError("Failed to apply segment", err.Error())
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

	blocks := segmentBlocks(state, true)
	if err := r.client.DeleteManagedBlocks(ctx, blocks); err != nil {
		resp.Diagnostics.AddError("Failed to delete segment", err.Error())
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func applyDefaults(plan *ResourceModel) {
	if plan.Proto.IsNull() || plan.Proto.IsUnknown() || plan.Proto.ValueString() == "" {
		plan.Proto = types.StringValue("static")
	}
	if plan.Zone.IsNull() || plan.Zone.IsUnknown() || plan.Zone.ValueString() == "" {
		plan.Zone = types.StringValue(plan.Name.ValueString())
	}
	if plan.DHCPLeaseTime.IsNull() || plan.DHCPLeaseTime.IsUnknown() || plan.DHCPLeaseTime.ValueString() == "" {
		plan.DHCPLeaseTime = types.StringValue("12h")
	}
	if plan.FirewallInput.IsNull() || plan.FirewallInput.IsUnknown() || plan.FirewallInput.ValueString() == "" {
		plan.FirewallInput = types.StringValue("REJECT")
	}
	if plan.FirewallOutput.IsNull() || plan.FirewallOutput.IsUnknown() || plan.FirewallOutput.ValueString() == "" {
		plan.FirewallOutput = types.StringValue("ACCEPT")
	}
	if plan.FirewallForward.IsNull() || plan.FirewallForward.IsUnknown() || plan.FirewallForward.ValueString() == "" {
		plan.FirewallForward = types.StringValue("REJECT")
	}
	if plan.AllowWanForward.IsNull() || plan.AllowWanForward.IsUnknown() {
		plan.AllowWanForward = types.BoolValue(false)
	}
}

func validatePlan(plan ResourceModel, diags *diag.Diagnostics) bool {
	if strings.TrimSpace(plan.Name.ValueString()) == "" {
		diags.AddError("Invalid name", "name cannot be empty.")
	}
	if strings.TrimSpace(plan.Device.ValueString()) == "" {
		diags.AddError("Invalid device", "device cannot be empty.")
	}
	if _, _, err := net.ParseCIDR(plan.CIDR.ValueString()); err != nil {
		diags.AddError("Invalid CIDR", fmt.Sprintf("cidr must be valid: %v", err))
	}
	if plan.DHCPStart.ValueInt64() < 1 {
		diags.AddError("Invalid dhcp_start", "dhcp_start must be >= 1.")
	}
	if plan.DHCPLimit.ValueInt64() < 1 {
		diags.AddError("Invalid dhcp_limit", "dhcp_limit must be >= 1.")
	}
	if plan.VLANID.ValueInt64() != 0 {
		if plan.VLANID.ValueInt64() < 1 || plan.VLANID.ValueInt64() > 4094 {
			diags.AddError("Invalid vlan_id", "vlan_id must be between 1 and 4094.")
		}
		if strings.TrimSpace(plan.ParentInterface.ValueString()) == "" {
			diags.AddError("Missing parent_interface", "parent_interface is required when vlan_id is set.")
		}
	}
	return !diags.HasError()
}

func segmentBlocks(plan ResourceModel, includeOptionalDeletes bool) []luci.ManagedBlock {
	name := plan.Name.ValueString()
	zone := plan.Zone.ValueString()

	networkBlock := buildNetworkBlock(plan)
	dhcpBlock := buildDHCPPoolBlock(plan)
	zoneBlock := buildFirewallZoneBlock(plan)

	blocks := []luci.ManagedBlock{
		{Package: "network", Service: "network", Key: "segment-network:" + name, Block: networkBlock},
		{Package: "dhcp", Service: "dnsmasq", Key: "segment-dhcp:" + name, Block: dhcpBlock},
		{Package: "firewall", Service: "firewall", Key: "segment-fw-zone:" + zone, Block: zoneBlock},
	}

	forwardKey := "segment-fw-forward:" + zone + "->wan"
	if plan.AllowWanForward.ValueBool() {
		blocks = append(blocks, luci.ManagedBlock{
			Package: "firewall",
			Service: "firewall",
			Key:     forwardKey,
			Block:   "config forwarding\n\toption src '" + zone + "'\n\toption dest 'wan'",
		})
	} else if includeOptionalDeletes {
		blocks = append(blocks, luci.ManagedBlock{
			Package: "firewall",
			Service: "firewall",
			Key:     forwardKey,
		})
	}
	return blocks
}

func buildNetworkBlock(plan ResourceModel) string {
	ip, ipNet, _ := net.ParseCIDR(plan.CIDR.ValueString())
	lines := []string{
		"config interface '" + plan.Name.ValueString() + "'",
		"\toption device '" + plan.Device.ValueString() + "'",
		"\toption proto '" + plan.Proto.ValueString() + "'",
		"\toption ipaddr '" + ip.String() + "'",
		"\toption netmask '" + net.IP(ipNet.Mask).String() + "'",
	}
	if plan.Gateway.ValueString() != "" {
		lines = append(lines, "\toption gateway '"+plan.Gateway.ValueString()+"'")
	}
	if !plan.DNS.IsNull() && !plan.DNS.IsUnknown() {
		var dns []string
		_ = plan.DNS.ElementsAs(context.Background(), &dns, false)
		for _, d := range dns {
			if strings.TrimSpace(d) != "" {
				lines = append(lines, "\tlist dns '"+d+"'")
			}
		}
	}
	return strings.Join(lines, "\n")
}

func buildDHCPPoolBlock(plan ResourceModel) string {
	return strings.Join([]string{
		"config dhcp '" + plan.Name.ValueString() + "'",
		"\toption interface '" + plan.Name.ValueString() + "'",
		fmt.Sprintf("\toption start '%d'", plan.DHCPStart.ValueInt64()),
		fmt.Sprintf("\toption limit '%d'", plan.DHCPLimit.ValueInt64()),
		"\toption leasetime '" + plan.DHCPLeaseTime.ValueString() + "'",
	}, "\n")
}

func buildFirewallZoneBlock(plan ResourceModel) string {
	return strings.Join([]string{
		"config zone",
		"\toption name '" + plan.Zone.ValueString() + "'",
		"\tlist network '" + plan.Name.ValueString() + "'",
		"\toption input '" + plan.FirewallInput.ValueString() + "'",
		"\toption output '" + plan.FirewallOutput.ValueString() + "'",
		"\toption forward '" + plan.FirewallForward.ValueString() + "'",
	}, "\n")
}
