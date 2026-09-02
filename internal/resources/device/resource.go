package device

import (
	"context"
	"fmt"
	"strings"

	"github.com/h3ow3d/terraform-provider-openwrt/internal/client/luci"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type Resource struct{ client luci.Client }

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
			"id":    schema.StringAttribute{Computed: true},
			"name":  schema.StringAttribute{Required: true},
			"type":  schema.StringAttribute{Required: true},
			"ports": schema.ListAttribute{Optional: true, ElementType: types.StringType},
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
	if !validatePlan(plan, &resp.Diagnostics) {
		return
	}
	if err := r.client.ApplyManagedBlocks(ctx, []luci.ManagedBlock{{
		Package: "network",
		Service: "network",
		Key:     "device:" + plan.Name.ValueString(),
		Block:   buildDeviceBlock(plan),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to create device", err.Error())
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
	if !validatePlan(plan, &resp.Diagnostics) {
		return
	}
	if err := r.client.ApplyManagedBlocks(ctx, []luci.ManagedBlock{{
		Package: "network",
		Service: "network",
		Key:     "device:" + plan.Name.ValueString(),
		Block:   buildDeviceBlock(plan),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to update device", err.Error())
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
		Package: "network",
		Service: "network",
		Key:     "device:" + state.Name.ValueString(),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to delete device", err.Error())
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func validatePlan(plan ResourceModel, diags *diag.Diagnostics) bool {
	name := strings.TrimSpace(plan.Name.ValueString())
	if name == "" {
		diags.AddError("Invalid device name", "name cannot be empty.")
	}
	deviceType := strings.TrimSpace(plan.Type.ValueString())
	if deviceType == "" {
		diags.AddError("Invalid device type", "type cannot be empty.")
	}
	return !diags.HasError()
}

func buildDeviceBlock(plan ResourceModel) string {
	lines := []string{
		"config device '" + plan.Name.ValueString() + "'",
		"\toption name '" + plan.Name.ValueString() + "'",
		"\toption type '" + plan.Type.ValueString() + "'",
	}
	if !plan.Ports.IsNull() && !plan.Ports.IsUnknown() {
		var ports []string
		_ = plan.Ports.ElementsAs(context.Background(), &ports, false)
		for _, p := range ports {
			p = strings.TrimSpace(p)
			if p != "" {
				lines = append(lines, "\tlist ports '"+p+"'")
			}
		}
	}
	return strings.Join(lines, "\n")
}
