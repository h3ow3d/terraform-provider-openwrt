package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/h3ow3d/terraform-provider-openwrt/internal/client/luci"
	"github.com/h3ow3d/terraform-provider-openwrt/internal/resources/dhcphost"
	"github.com/h3ow3d/terraform-provider-openwrt/internal/resources/dhcppool"
	"github.com/h3ow3d/terraform-provider-openwrt/internal/resources/firewallrule"
	"github.com/h3ow3d/terraform-provider-openwrt/internal/resources/network"
	"github.com/h3ow3d/terraform-provider-openwrt/internal/resources/segment"
	"github.com/h3ow3d/terraform-provider-openwrt/internal/resources/wireguardinterface"
	"github.com/h3ow3d/terraform-provider-openwrt/internal/resources/wireguardpeer"
)

var _ provider.Provider = (*OpenWRTProvider)(nil)

type OpenWRTProvider struct {
	version string
}

type OpenWRTProviderModel struct {
	Remote   types.String `tfsdk:"remote"`
	User     types.String `tfsdk:"user"`
	Password types.String `tfsdk:"password"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &OpenWRTProvider{version: version}
	}
}

func (p *OpenWRTProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "openwrt"
	resp.Version = p.version
}

func (p *OpenWRTProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Provider for orchestrating OpenWrt network configuration over LuCI RPC.",
		Attributes: map[string]schema.Attribute{
			"remote": schema.StringAttribute{
				Optional:    true,
				Description: "OpenWrt LuCI RPC endpoint. Can also be set via OPENWRT_REMOTE.",
			},
			"user": schema.StringAttribute{
				Optional:    true,
				Description: "OpenWrt username. Can also be set via OPENWRT_USER.",
			},
			"password": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "OpenWrt password. Can also be set via OPENWRT_PASSWORD.",
			},
		},
	}
}

func (p *OpenWRTProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data OpenWRTProviderModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	remote := firstNonEmpty(data.Remote.ValueString(), os.Getenv("OPENWRT_REMOTE"))
	user := firstNonEmpty(data.User.ValueString(), os.Getenv("OPENWRT_USER"))
	password := firstNonEmpty(data.Password.ValueString(), os.Getenv("OPENWRT_PASSWORD"))

	if remote == "" {
		resp.Diagnostics.AddError("Missing OpenWrt endpoint", "Provider attribute 'remote' is required (or set OPENWRT_REMOTE).")
	}
	if user == "" {
		resp.Diagnostics.AddError("Missing OpenWrt username", "Provider attribute 'user' is required (or set OPENWRT_USER).")
	}
	if password == "" {
		resp.Diagnostics.AddError("Missing OpenWrt password", "Provider attribute 'password' is required (or set OPENWRT_PASSWORD).")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	client := luci.NewClient(luci.Config{
		Remote:   remote,
		User:     user,
		Password: password,
	})

	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *OpenWRTProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return nil
}

func (p *OpenWRTProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		segment.NewResource,
		network.NewResource,
		dhcppool.NewResource,
		dhcphost.NewResource,
		firewallrule.NewResource,
		wireguardinterface.NewResource,
		wireguardpeer.NewResource,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
