package domain

import (
	"context"
	"fmt"
	"net"
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
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
	IP   types.String `tfsdk:"ip"`
}

func NewResource() resource.Resource { return &Resource{} }

func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain"
}

func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Static domain record (config domain) in /etc/config/dhcp.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true},
			"name": schema.StringAttribute{Required: true},
			"ip":   schema.StringAttribute{Required: true},
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
		Package: "dhcp",
		Service: "dnsmasq",
		Key:     "domain:" + plan.Name.ValueString(),
		Block:   buildDomainBlock(plan),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to create domain", err.Error())
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
		Package: "dhcp",
		Service: "dnsmasq",
		Key:     "domain:" + plan.Name.ValueString(),
		Block:   buildDomainBlock(plan),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to update domain", err.Error())
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
		Key:     "domain:" + state.Name.ValueString(),
	}}); err != nil {
		resp.Diagnostics.AddError("Failed to delete domain", err.Error())
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func validatePlan(plan ResourceModel, diags *diag.Diagnostics) bool {
	if strings.TrimSpace(plan.Name.ValueString()) == "" {
		diags.AddError("Invalid domain name", "name cannot be empty.")
	}
	if ip := net.ParseIP(strings.TrimSpace(plan.IP.ValueString())); ip == nil {
		diags.AddError("Invalid IP address", "ip must be a valid IPv4 or IPv6 address.")
	}
	return !diags.HasError()
}

func buildDomainBlock(plan ResourceModel) string {
	return strings.Join([]string{
		"config domain",
		"\toption name '" + plan.Name.ValueString() + "'",
		"\toption ip '" + plan.IP.ValueString() + "'",
	}, "\n")
}
