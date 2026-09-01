package network

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
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	Device   types.String `tfsdk:"device"`
	Proto    types.String `tfsdk:"proto"`
	CIDR     types.String `tfsdk:"cidr"`
	Zone     types.String `tfsdk:"zone"`
	MTU      types.Int64  `tfsdk:"mtu"`
	DNS      types.List   `tfsdk:"dns"`
	Gateway  types.String `tfsdk:"gateway"`
	VLANID   types.Int64  `tfsdk:"vlan_id"`
	ParentIF types.String `tfsdk:"parent_interface"`
}

func NewResource() resource.Resource {
	return &Resource{}
}

func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Defines an OpenWrt network (interface + optional VLAN metadata).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Stable resource id.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "OpenWrt network name (UCI section key).",
			},
			"device": schema.StringAttribute{
				Required:    true,
				Description: "Underlying OpenWrt device, e.g. br-lan or eth0.20.",
			},
			"proto": schema.StringAttribute{
				Required:    true,
				Description: "Protocol, e.g. static, dhcp, pppoe.",
			},
			"cidr": schema.StringAttribute{
				Optional:    true,
				Description: "CIDR address for static interfaces, e.g. 192.168.20.1/24.",
			},
			"zone": schema.StringAttribute{
				Optional:    true,
				Description: "Firewall zone association.",
			},
			"mtu": schema.Int64Attribute{
				Optional:    true,
				Description: "Interface MTU.",
			},
			"dns": schema.ListAttribute{
				Optional:    true,
				ElementType: types.StringType,
				Description: "DNS resolver IPs.",
			},
			"gateway": schema.StringAttribute{
				Optional:    true,
				Description: "Gateway address.",
			},
			"vlan_id": schema.Int64Attribute{
				Optional:    true,
				Description: "Optional VLAN identifier.",
			},
			"parent_interface": schema.StringAttribute{
				Optional:    true,
				Description: "Parent interface for VLAN-tagged links.",
			},
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

	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}
	if !validatePlan(plan, &resp.Diagnostics) {
		return
	}

	if err := r.client.ApplyNetwork(ctx, toApplyNetworkRequest(plan)); err != nil {
		resp.Diagnostics.AddError("Failed to create network", err.Error())
		return
	}

	plan.ID = types.StringValue(plan.Name.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
}

func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}
	if !validatePlan(plan, &resp.Diagnostics) {
		return
	}

	if err := r.client.ApplyNetwork(ctx, toApplyNetworkRequest(plan)); err != nil {
		resp.Diagnostics.AddError("Failed to update network", err.Error())
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

	if err := r.client.DeleteNetwork(ctx, state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Failed to delete network", err.Error())
		return
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func toApplyNetworkRequest(plan ResourceModel) luci.ApplyNetworkRequest {
	req := luci.ApplyNetworkRequest{
		ID:       plan.ID.ValueString(),
		Name:     plan.Name.ValueString(),
		Device:   plan.Device.ValueString(),
		Proto:    plan.Proto.ValueString(),
		CIDR:     plan.CIDR.ValueString(),
		Zone:     plan.Zone.ValueString(),
		MTU:      plan.MTU.ValueInt64(),
		Gateway:  plan.Gateway.ValueString(),
		VLANID:   plan.VLANID.ValueInt64(),
		ParentIF: plan.ParentIF.ValueString(),
	}

	if !plan.DNS.IsNull() && !plan.DNS.IsUnknown() {
		var dns []string
		_ = plan.DNS.ElementsAs(context.Background(), &dns, false)
		req.DNS = dns
	}
	return req
}

func validatePlan(plan ResourceModel, diags *diag.Diagnostics) bool {
	if strings.TrimSpace(plan.Name.ValueString()) == "" {
		diags.AddError("Invalid name", "name cannot be empty.")
	}
	if strings.TrimSpace(plan.Device.ValueString()) == "" {
		diags.AddError("Invalid device", "device cannot be empty.")
	}
	proto := strings.TrimSpace(plan.Proto.ValueString())
	if proto == "" {
		diags.AddError("Invalid protocol", "proto cannot be empty.")
	}
	if plan.CIDR.ValueString() != "" {
		if _, _, err := net.ParseCIDR(plan.CIDR.ValueString()); err != nil {
			diags.AddError("Invalid CIDR", fmt.Sprintf("cidr must be valid (example: 192.168.20.1/24): %v", err))
		}
	}
	if plan.VLANID.ValueInt64() != 0 {
		if plan.VLANID.ValueInt64() < 1 || plan.VLANID.ValueInt64() > 4094 {
			diags.AddError("Invalid VLAN ID", "vlan_id must be between 1 and 4094.")
		}
		if strings.TrimSpace(plan.ParentIF.ValueString()) == "" {
			diags.AddError("Missing parent interface", "parent_interface is required when vlan_id is set.")
		}
	}
	return !diags.HasError()
}
