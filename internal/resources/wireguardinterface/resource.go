package wireguardinterface

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/ophomelab/terraform-provider-openwrt/internal/client/luci"
)

type Resource struct{ client luci.Client }

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
			"id":          schema.StringAttribute{Computed: true},
			"name":        schema.StringAttribute{Required: true},
			"private_key": schema.StringAttribute{Required: true, Sensitive: true},
			"listen_port": schema.Int64Attribute{Optional: true},
			"addresses":   schema.ListAttribute{Optional: true, ElementType: types.StringType},
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
	if strings.TrimSpace(plan.Name.ValueString()) == "" || strings.TrimSpace(plan.PrivateKey.ValueString()) == "" {
		resp.Diagnostics.AddError("Invalid WireGuard interface", "name and private_key are required.")
		return
	}
	lines := []string{
		"config interface '" + plan.Name.ValueString() + "'",
		"\toption proto 'wireguard'",
		"\toption private_key '" + plan.PrivateKey.ValueString() + "'",
	}
	if plan.ListenPort.ValueInt64() > 0 {
		lines = append(lines, fmt.Sprintf("\toption listen_port '%d'", plan.ListenPort.ValueInt64()))
	}
	if !plan.Addresses.IsNull() && !plan.Addresses.IsUnknown() {
		var addresses []string
		_ = plan.Addresses.ElementsAs(context.Background(), &addresses, false)
		for _, address := range addresses {
			if strings.TrimSpace(address) != "" {
				lines = append(lines, "\tlist addresses '"+address+"'")
			}
		}
	}
	if err := r.client.ApplyManagedBlocks(ctx, []luci.ManagedBlock{{
		Package: "network",
		Service: "network",
		Key:     "wireguard-interface:" + plan.Name.ValueString(),
		Block:   strings.Join(lines, "\n"),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to create WireGuard interface", err.Error())
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
	lines := []string{
		"config interface '" + plan.Name.ValueString() + "'",
		"\toption proto 'wireguard'",
		"\toption private_key '" + plan.PrivateKey.ValueString() + "'",
	}
	if plan.ListenPort.ValueInt64() > 0 {
		lines = append(lines, fmt.Sprintf("\toption listen_port '%d'", plan.ListenPort.ValueInt64()))
	}
	if !plan.Addresses.IsNull() && !plan.Addresses.IsUnknown() {
		var addresses []string
		_ = plan.Addresses.ElementsAs(context.Background(), &addresses, false)
		for _, address := range addresses {
			if strings.TrimSpace(address) != "" {
				lines = append(lines, "\tlist addresses '"+address+"'")
			}
		}
	}
	if err := r.client.ApplyManagedBlocks(ctx, []luci.ManagedBlock{{
		Package: "network",
		Service: "network",
		Key:     "wireguard-interface:" + plan.Name.ValueString(),
		Block:   strings.Join(lines, "\n"),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to update WireGuard interface", err.Error())
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
		Key:     "wireguard-interface:" + state.Name.ValueString(),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to delete WireGuard interface", err.Error())
	}
}
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
