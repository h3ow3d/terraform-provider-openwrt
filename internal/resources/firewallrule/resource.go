package firewallrule

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

type Resource struct {
	client luci.Client
}

type ResourceModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	Src      types.String `tfsdk:"src"`
	Dest     types.String `tfsdk:"dest"`
	Proto    types.String `tfsdk:"proto"`
	DestPort types.String `tfsdk:"dest_port"`
	Target   types.String `tfsdk:"target"`
	Family   types.String `tfsdk:"family"`
}

func NewResource() resource.Resource { return &Resource{} }

func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall_rule"
}

func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Firewall rule in /etc/config/firewall.",
		Attributes: map[string]schema.Attribute{
			"id":        schema.StringAttribute{Computed: true},
			"name":      schema.StringAttribute{Required: true},
			"src":       schema.StringAttribute{Required: true},
			"dest":      schema.StringAttribute{Optional: true},
			"proto":     schema.StringAttribute{Optional: true, Computed: true},
			"dest_port": schema.StringAttribute{Optional: true},
			"target":    schema.StringAttribute{Optional: true, Computed: true},
			"family":    schema.StringAttribute{Optional: true},
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

	if plan.Proto.IsNull() || plan.Proto.IsUnknown() || plan.Proto.ValueString() == "" {
		plan.Proto = types.StringValue("tcp udp")
	}
	if plan.Target.IsNull() || plan.Target.IsUnknown() || plan.Target.ValueString() == "" {
		plan.Target = types.StringValue("ACCEPT")
	}
	if strings.TrimSpace(plan.Name.ValueString()) == "" || strings.TrimSpace(plan.Src.ValueString()) == "" {
		resp.Diagnostics.AddError("Invalid firewall rule", "name and src are required.")
		return
	}
	block := []string{
		"config rule",
		"\toption name '" + plan.Name.ValueString() + "'",
		"\toption src '" + plan.Src.ValueString() + "'",
		"\toption proto '" + plan.Proto.ValueString() + "'",
		"\toption target '" + plan.Target.ValueString() + "'",
	}
	if plan.Dest.ValueString() != "" {
		block = append(block, "\toption dest '"+plan.Dest.ValueString()+"'")
	}
	if plan.DestPort.ValueString() != "" {
		block = append(block, "\toption dest_port '"+plan.DestPort.ValueString()+"'")
	}
	if plan.Family.ValueString() != "" {
		block = append(block, "\toption family '"+plan.Family.ValueString()+"'")
	}
	if err := r.client.ApplyManagedBlocks(ctx, []luci.ManagedBlock{{
		Package: "firewall",
		Service: "firewall",
		Key:     "firewall-rule:" + plan.Name.ValueString(),
		Block:   strings.Join(block, "\n"),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to apply firewall rule", err.Error())
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

	if plan.Proto.IsNull() || plan.Proto.IsUnknown() || plan.Proto.ValueString() == "" {
		plan.Proto = types.StringValue("tcp udp")
	}
	if plan.Target.IsNull() || plan.Target.IsUnknown() || plan.Target.ValueString() == "" {
		plan.Target = types.StringValue("ACCEPT")
	}
	if strings.TrimSpace(plan.Name.ValueString()) == "" || strings.TrimSpace(plan.Src.ValueString()) == "" {
		resp.Diagnostics.AddError("Invalid firewall rule", "name and src are required.")
		return
	}
	block := []string{
		"config rule",
		"\toption name '" + plan.Name.ValueString() + "'",
		"\toption src '" + plan.Src.ValueString() + "'",
		"\toption proto '" + plan.Proto.ValueString() + "'",
		"\toption target '" + plan.Target.ValueString() + "'",
	}
	if plan.Dest.ValueString() != "" {
		block = append(block, "\toption dest '"+plan.Dest.ValueString()+"'")
	}
	if plan.DestPort.ValueString() != "" {
		block = append(block, "\toption dest_port '"+plan.DestPort.ValueString()+"'")
	}
	if plan.Family.ValueString() != "" {
		block = append(block, "\toption family '"+plan.Family.ValueString()+"'")
	}
	if err := r.client.ApplyManagedBlocks(ctx, []luci.ManagedBlock{{
		Package: "firewall",
		Service: "firewall",
		Key:     "firewall-rule:" + plan.Name.ValueString(),
		Block:   strings.Join(block, "\n"),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to apply firewall rule", err.Error())
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
		Package: "firewall",
		Service: "firewall",
		Key:     "firewall-rule:" + state.Name.ValueString(),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to delete firewall rule", err.Error())
	}
}
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
