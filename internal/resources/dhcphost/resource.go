package dhcphost

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/h3ow3d/terraform-provider-openwrt/internal/client/luci"
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
	MAC      types.String `tfsdk:"mac"`
	IP       types.String `tfsdk:"ip"`
	DUID     types.String `tfsdk:"duid"`
	Hostname types.String `tfsdk:"hostname"`
}

func NewResource() resource.Resource {
	return &Resource{}
}

func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dhcp_host"
}

func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Defines a DHCP host reservation on OpenWrt.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Stable resource id.",
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Reservation name.",
			},
			"mac": schema.StringAttribute{
				Required:    true,
				Description: "MAC address.",
			},
			"ip": schema.StringAttribute{
				Required:    true,
				Description: "Reserved IPv4 address.",
			},
			"duid": schema.StringAttribute{
				Optional:    true,
				Description: "Optional DHCPv6 DUID.",
			},
			"hostname": schema.StringAttribute{
				Optional:    true,
				Description: "Optional DNS hostname.",
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

	if err := r.client.UpsertDHCPHost(ctx, toDHCPHostRequest(plan)); err != nil {
		resp.Diagnostics.AddError("Failed to create DHCP reservation", err.Error())
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

	if err := r.client.UpsertDHCPHost(ctx, toDHCPHostRequest(plan)); err != nil {
		resp.Diagnostics.AddError("Failed to update DHCP reservation", err.Error())
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

	if err := r.client.DeleteDHCPHost(ctx, state.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Failed to delete DHCP reservation", err.Error())
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func toDHCPHostRequest(plan ResourceModel) luci.UpsertDHCPHostRequest {
	return luci.UpsertDHCPHostRequest{
		ID:       plan.ID.ValueString(),
		Name:     plan.Name.ValueString(),
		MAC:      strings.ToLower(plan.MAC.ValueString()),
		IP:       plan.IP.ValueString(),
		DUID:     plan.DUID.ValueString(),
		Hostname: plan.Hostname.ValueString(),
	}
}

func validatePlan(plan ResourceModel, diags *diag.Diagnostics) bool {
	if strings.TrimSpace(plan.Name.ValueString()) == "" {
		diags.AddError("Invalid name", "name cannot be empty.")
	}
	if !regexp.MustCompile(`^([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}$`).MatchString(plan.MAC.ValueString()) {
		diags.AddError("Invalid MAC address", "mac must be in format aa:bb:cc:dd:ee:ff.")
	}
	ip := net.ParseIP(plan.IP.ValueString())
	if ip == nil || ip.To4() == nil {
		diags.AddError("Invalid IPv4 address", "ip must be a valid IPv4 address.")
	}
	return !diags.HasError()
}
