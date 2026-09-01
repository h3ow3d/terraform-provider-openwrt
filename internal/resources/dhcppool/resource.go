package dhcppool

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/h3ow3d/terraform-provider-openwrt/internal/client/luci"
)

type Resource struct{ client luci.Client }

type ResourceModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Interface types.String `tfsdk:"interface"`
	Start     types.Int64  `tfsdk:"start"`
	Limit     types.Int64  `tfsdk:"limit"`
	LeaseTime types.String `tfsdk:"leasetime"`
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
	if strings.TrimSpace(plan.Name.ValueString()) == "" || strings.TrimSpace(plan.Interface.ValueString()) == "" {
		resp.Diagnostics.AddError("Invalid DHCP pool", "name and interface are required.")
		return
	}
	block := strings.Join([]string{
		"config dhcp '" + plan.Name.ValueString() + "'",
		"\toption interface '" + plan.Interface.ValueString() + "'",
		fmt.Sprintf("\toption start '%d'", plan.Start.ValueInt64()),
		fmt.Sprintf("\toption limit '%d'", plan.Limit.ValueInt64()),
		"\toption leasetime '" + plan.LeaseTime.ValueString() + "'",
	}, "\n")
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
	if strings.TrimSpace(plan.Name.ValueString()) == "" || strings.TrimSpace(plan.Interface.ValueString()) == "" {
		resp.Diagnostics.AddError("Invalid DHCP pool", "name and interface are required.")
		return
	}
	block := strings.Join([]string{
		"config dhcp '" + plan.Name.ValueString() + "'",
		"\toption interface '" + plan.Interface.ValueString() + "'",
		fmt.Sprintf("\toption start '%d'", plan.Start.ValueInt64()),
		fmt.Sprintf("\toption limit '%d'", plan.Limit.ValueInt64()),
		"\toption leasetime '" + plan.LeaseTime.ValueString() + "'",
	}, "\n")
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
