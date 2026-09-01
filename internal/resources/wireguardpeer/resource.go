package wireguardpeer

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
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	Interface           types.String `tfsdk:"interface"`
	PublicKey           types.String `tfsdk:"public_key"`
	PresharedKey        types.String `tfsdk:"preshared_key"`
	AllowedIPs          types.List   `tfsdk:"allowed_ips"`
	EndpointHost        types.String `tfsdk:"endpoint_host"`
	EndpointPort        types.Int64  `tfsdk:"endpoint_port"`
	PersistentKeepalive types.Int64  `tfsdk:"persistent_keepalive"`
}

func NewResource() resource.Resource { return &Resource{} }
func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_wireguard_peer"
}
func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "WireGuard peer section in /etc/config/network.",
		Attributes: map[string]schema.Attribute{
			"id":                   schema.StringAttribute{Computed: true},
			"name":                 schema.StringAttribute{Required: true},
			"interface":            schema.StringAttribute{Required: true},
			"public_key":           schema.StringAttribute{Required: true},
			"preshared_key":        schema.StringAttribute{Optional: true, Sensitive: true},
			"allowed_ips":          schema.ListAttribute{Optional: true, ElementType: types.StringType},
			"endpoint_host":        schema.StringAttribute{Optional: true},
			"endpoint_port":        schema.Int64Attribute{Optional: true},
			"persistent_keepalive": schema.Int64Attribute{Optional: true},
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
	if strings.TrimSpace(plan.Name.ValueString()) == "" || strings.TrimSpace(plan.Interface.ValueString()) == "" || strings.TrimSpace(plan.PublicKey.ValueString()) == "" {
		resp.Diagnostics.AddError("Invalid WireGuard peer", "name, interface and public_key are required.")
		return
	}
	lines := []string{
		"config wireguard_" + plan.Interface.ValueString(),
		"\toption description '" + plan.Name.ValueString() + "'",
		"\toption public_key '" + plan.PublicKey.ValueString() + "'",
	}
	if plan.PresharedKey.ValueString() != "" {
		lines = append(lines, "\toption preshared_key '"+plan.PresharedKey.ValueString()+"'")
	}
	if !plan.AllowedIPs.IsNull() && !plan.AllowedIPs.IsUnknown() {
		var allowedIPs []string
		_ = plan.AllowedIPs.ElementsAs(context.Background(), &allowedIPs, false)
		for _, ip := range allowedIPs {
			if strings.TrimSpace(ip) != "" {
				lines = append(lines, "\tlist allowed_ips '"+ip+"'")
			}
		}
	}
	if plan.EndpointHost.ValueString() != "" {
		lines = append(lines, "\toption endpoint_host '"+plan.EndpointHost.ValueString()+"'")
	}
	if plan.EndpointPort.ValueInt64() > 0 {
		lines = append(lines, fmt.Sprintf("\toption endpoint_port '%d'", plan.EndpointPort.ValueInt64()))
	}
	if plan.PersistentKeepalive.ValueInt64() > 0 {
		lines = append(lines, fmt.Sprintf("\toption persistent_keepalive '%d'", plan.PersistentKeepalive.ValueInt64()))
	}
	if err := r.client.ApplyManagedBlocks(ctx, []luci.ManagedBlock{{
		Package: "network",
		Service: "network",
		Key:     "wireguard-peer:" + plan.Interface.ValueString() + ":" + plan.Name.ValueString(),
		Block:   strings.Join(lines, "\n"),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to apply WireGuard peer", err.Error())
		return
	}
	plan.ID = types.StringValue(plan.Interface.ValueString() + "/" + plan.Name.ValueString())
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
	if strings.TrimSpace(plan.Name.ValueString()) == "" || strings.TrimSpace(plan.Interface.ValueString()) == "" || strings.TrimSpace(plan.PublicKey.ValueString()) == "" {
		resp.Diagnostics.AddError("Invalid WireGuard peer", "name, interface and public_key are required.")
		return
	}
	lines := []string{
		"config wireguard_" + plan.Interface.ValueString(),
		"\toption description '" + plan.Name.ValueString() + "'",
		"\toption public_key '" + plan.PublicKey.ValueString() + "'",
	}
	if plan.PresharedKey.ValueString() != "" {
		lines = append(lines, "\toption preshared_key '"+plan.PresharedKey.ValueString()+"'")
	}
	if !plan.AllowedIPs.IsNull() && !plan.AllowedIPs.IsUnknown() {
		var allowedIPs []string
		_ = plan.AllowedIPs.ElementsAs(context.Background(), &allowedIPs, false)
		for _, ip := range allowedIPs {
			if strings.TrimSpace(ip) != "" {
				lines = append(lines, "\tlist allowed_ips '"+ip+"'")
			}
		}
	}
	if plan.EndpointHost.ValueString() != "" {
		lines = append(lines, "\toption endpoint_host '"+plan.EndpointHost.ValueString()+"'")
	}
	if plan.EndpointPort.ValueInt64() > 0 {
		lines = append(lines, fmt.Sprintf("\toption endpoint_port '%d'", plan.EndpointPort.ValueInt64()))
	}
	if plan.PersistentKeepalive.ValueInt64() > 0 {
		lines = append(lines, fmt.Sprintf("\toption persistent_keepalive '%d'", plan.PersistentKeepalive.ValueInt64()))
	}
	if err := r.client.ApplyManagedBlocks(ctx, []luci.ManagedBlock{{
		Package: "network",
		Service: "network",
		Key:     "wireguard-peer:" + plan.Interface.ValueString() + ":" + plan.Name.ValueString(),
		Block:   strings.Join(lines, "\n"),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to apply WireGuard peer", err.Error())
		return
	}
	plan.ID = types.StringValue(plan.Interface.ValueString() + "/" + plan.Name.ValueString())
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
		Key:     "wireguard-peer:" + state.Interface.ValueString() + ":" + state.Name.ValueString(),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to delete WireGuard peer", err.Error())
	}
}
func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
