package dhcppool

import (
	"context"
	"fmt"
	"strings"

	"github.com/h3ow3d/terraform-provider-openwrt/internal/client/luci"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type Resource struct{ client luci.Client }

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
			"id":        schema.StringAttribute{Computed: true},
			"name":      schema.StringAttribute{Required: true},
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
	c, ok := req.ProviderData.(luci.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected luci.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}
func (r *Resource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.LeaseTime.IsNull() || plan.LeaseTime.IsUnknown() || plan.LeaseTime.ValueString() == "" {
		plan.LeaseTime = types.StringValue("12h")
	}
	if plan.Force.IsNull() || plan.Force.IsUnknown() {
		plan.Force = types.BoolValue(true)
	}
	if plan.DHCPv6.IsNull() || plan.DHCPv6.IsUnknown() || plan.DHCPv6.ValueString() == "" {
		plan.DHCPv6 = types.StringValue("disabled")
	}
	if plan.RA.IsNull() || plan.RA.IsUnknown() || plan.RA.ValueString() == "" {
		plan.RA = types.StringValue("disabled")
	}
	if strings.TrimSpace(plan.Name.ValueString()) == "" || strings.TrimSpace(plan.Interface.ValueString()) == "" {
		resp.Diagnostics.AddError("Invalid DHCP pool", "name and interface are required.")
		return
	}
	blockLines := []string{
		"config dhcp '" + plan.Name.ValueString() + "'",
		"\toption interface '" + plan.Interface.ValueString() + "'",
		fmt.Sprintf("\toption start '%d'", plan.Start.ValueInt64()),
		fmt.Sprintf("\toption limit '%d'", plan.Limit.ValueInt64()),
		"\toption leasetime '" + plan.LeaseTime.ValueString() + "'",
		"\toption dhcpv6 '" + plan.DHCPv6.ValueString() + "'",
		"\toption ra '" + plan.RA.ValueString() + "'",
	}
	if plan.Force.ValueBool() {
		blockLines = append(blockLines, "\toption force '1'")
	} else {
		blockLines = append(blockLines, "\toption force '0'")
	}
	block := strings.Join(blockLines, "\n")
	if err := r.client.ApplyManagedBlocks(ctx, []luci.ManagedBlock{{
		Package: "dhcp",
		Service: "dnsmasq",
		Key:     "dhcp-pool:" + plan.Name.ValueString(),
		Block:   block,
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to create DHCP pool", err.Error())
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
	if plan.LeaseTime.IsNull() || plan.LeaseTime.IsUnknown() || plan.LeaseTime.ValueString() == "" {
		plan.LeaseTime = types.StringValue("12h")
	}
	if plan.Force.IsNull() || plan.Force.IsUnknown() {
		plan.Force = types.BoolValue(true)
	}
	if plan.DHCPv6.IsNull() || plan.DHCPv6.IsUnknown() || plan.DHCPv6.ValueString() == "" {
		plan.DHCPv6 = types.StringValue("disabled")
	}
	if plan.RA.IsNull() || plan.RA.IsUnknown() || plan.RA.ValueString() == "" {
		plan.RA = types.StringValue("disabled")
	}
	if strings.TrimSpace(plan.Name.ValueString()) == "" || strings.TrimSpace(plan.Interface.ValueString()) == "" {
		resp.Diagnostics.AddError("Invalid DHCP pool", "name and interface are required.")
		return
	}
	blockLines := []string{
		"config dhcp '" + plan.Name.ValueString() + "'",
		"\toption interface '" + plan.Interface.ValueString() + "'",
		fmt.Sprintf("\toption start '%d'", plan.Start.ValueInt64()),
		fmt.Sprintf("\toption limit '%d'", plan.Limit.ValueInt64()),
		"\toption leasetime '" + plan.LeaseTime.ValueString() + "'",
		"\toption dhcpv6 '" + plan.DHCPv6.ValueString() + "'",
		"\toption ra '" + plan.RA.ValueString() + "'",
	}
	if plan.Force.ValueBool() {
		blockLines = append(blockLines, "\toption force '1'")
	} else {
		blockLines = append(blockLines, "\toption force '0'")
	}
	block := strings.Join(blockLines, "\n")
	if err := r.client.ApplyManagedBlocks(ctx, []luci.ManagedBlock{{
		Package: "dhcp",
		Service: "dnsmasq",
		Key:     "dhcp-pool:" + plan.Name.ValueString(),
		Block:   block,
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to update DHCP pool", err.Error())
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
	if err := r.client.DeleteManagedBlocks(ctx, []luci.ManagedBlock{{
		Package: "dhcp",
		Service: "dnsmasq",
		Key:     "dhcp-pool:" + state.Name.ValueString(),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to delete DHCP pool", err.Error())
	}
}
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
