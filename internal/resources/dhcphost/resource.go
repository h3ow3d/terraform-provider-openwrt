package dhcphost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/h3ow3d/terraform-provider-openwrt/internal/client/modernubus"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	dhcpPackage            = "dhcp"
	hostSectionType        = "host"
	defaultApplyTimeoutSec = int64(10)
	sectionPrefix          = "tfhost_"
	sectionReadableMax     = 24
	sectionHashHexLen      = 16
	maxNameLength          = 253
)

var validNameChar = regexp.MustCompile(`^[a-z0-9._-]+$`)
var nonSectionChar = regexp.MustCompile(`[^a-z0-9_]+`)
var validDUID = regexp.MustCompile(`^[0-9a-f]+$`)

type ubusHostClient interface {
	CurrentRPCURL() (string, error)
	RunMutationTransaction(ctx context.Context, minLifetime time.Duration, fn func(context.Context) error) error
	UCIGet(ctx context.Context, req modernubus.UCIGetRequest) (modernubus.UCIGetResponse, error)
	UCIAdd(ctx context.Context, req modernubus.UCIAddRequest) (modernubus.UCIAddResponse, error)
	UCISet(ctx context.Context, req modernubus.UCISetRequest) (modernubus.UCISetResponse, error)
	UCIDelete(ctx context.Context, req modernubus.UCIDeleteRequest) (modernubus.UCIDeleteResponse, error)
	UCIApply(ctx context.Context, req modernubus.UCIApplyRequest) (modernubus.UCIApplyResponse, error)
	UCIConfirm(ctx context.Context, req modernubus.UCIConfirmRequest) (modernubus.UCIConfirmResponse, error)
}

type Resource struct {
	client ubusHostClient
}

type ResourceModel struct {
	ID       types.String `tfsdk:"id"`
	Name     types.String `tfsdk:"name"`
	MAC      types.String `tfsdk:"mac"`
	IP       types.String `tfsdk:"ip"`
	DUID     types.String `tfsdk:"duid"`
	Hostname types.String `tfsdk:"hostname"`
	DNS      types.Bool   `tfsdk:"dns"`
}

func NewResource() resource.Resource { return &Resource{} }

func (r *Resource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dhcp_host"
}

func (r *Resource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "DHCP host or static DNS record in /etc/config/dhcp.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"mac": schema.StringAttribute{
				Optional:    true,
				Description: "Optional MAC address for a DHCP reservation.",
			},
			"ip": schema.StringAttribute{
				Required: true,
			},
			"duid": schema.StringAttribute{
				Optional: true,
			},
			"hostname": schema.StringAttribute{
				Optional:    true,
				Description: "Optional hostname published by dnsmasq; defaults to name.",
			},
			"dns": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Description: "Whether dnsmasq publishes the host in local DNS. Defaults to true.",
			},
		},
	}
}

func (r *Resource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*modernubus.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data type", fmt.Sprintf("Expected *modernubus.Client, got %T", req.ProviderData))
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
	applyDefaults(&plan)
	desired, ok := normalizePlan(plan, &resp.Diagnostics)
	if !ok {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}
	if err := createHost(ctx, r.client, desired, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to create DHCP host", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &desired)...)
}

func (r *Resource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}
	name, err := canonicalName(state.Name.ValueString())
	if err != nil {
		resp.State.RemoveResource(ctx)
		return
	}
	live, exists, err := readLiveHost(ctx, r.client, name)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read DHCP host", err.Error())
		return
	}
	if !exists {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &live)...)
}

func (r *Resource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	applyDefaults(&plan)
	desired, ok := normalizePlan(plan, &resp.Diagnostics)
	if !ok {
		return
	}
	if r.client == nil {
		resp.Diagnostics.AddError("Unconfigured client", "Provider client is not configured.")
		return
	}
	if err := updateHost(ctx, r.client, desired, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to update DHCP host", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &desired)...)
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
	name, err := canonicalName(state.Name.ValueString())
	if err != nil {
		return
	}
	if err := deleteHost(ctx, r.client, name, defaultApplyTimeoutSec); err != nil {
		resp.Diagnostics.AddError("Failed to delete DHCP host", err.Error())
	}
}

func (r *Resource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	name, err := canonicalName(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import identifier", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

func createHost(ctx context.Context, client ubusHostClient, desired ResourceModel, applyTimeoutSec int64) error {
	name := desired.Name.ValueString()
	section := sectionNameForHost(name)
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: section})
		if err != nil {
			return err
		}
		if existing.SectionExists {
			return fmt.Errorf("section for DHCP host already exists; import this resource using identifier %q", name)
		}
		added, err := client.UCIAdd(txCtx, modernubus.UCIAddRequest{
			Config: dhcpPackage,
			Type:   hostSectionType,
			Name:   section,
			Values: hostValues(desired),
		})
		if err != nil {
			return err
		}
		if added.Section != section {
			return fmt.Errorf("uci.add created unexpected section name")
		}
		return applyVerifyConfirm(txCtx, ctx, client, desired, applyTimeoutSec)
	})
}

func updateHost(ctx context.Context, client ubusHostClient, desired ResourceModel, applyTimeoutSec int64) error {
	section := sectionNameForHost(desired.Name.ValueString())
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: section})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return fmt.Errorf("managed section for DHCP host is missing; run import or recreate")
		}
		for _, option := range optionalOptionsToDelete(existing, desired) {
			if _, err := client.UCIDelete(txCtx, modernubus.UCIDeleteRequest{Config: dhcpPackage, Section: section, Option: option}); err != nil {
				return err
			}
		}
		if _, err := client.UCISet(txCtx, modernubus.UCISetRequest{
			Config:  dhcpPackage,
			Section: section,
			Values:  hostValues(desired),
		}); err != nil {
			return err
		}
		return applyVerifyConfirm(txCtx, ctx, client, desired, applyTimeoutSec)
	})
}

func deleteHost(ctx context.Context, client ubusHostClient, name string, applyTimeoutSec int64) error {
	section := sectionNameForHost(name)
	return client.RunMutationTransaction(ctx, transactionLifetime(applyTimeoutSec), func(txCtx context.Context) error {
		existing, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: section})
		if err != nil {
			return err
		}
		if !existing.SectionExists {
			return nil
		}
		if _, err := client.UCIDelete(txCtx, modernubus.UCIDeleteRequest{Config: dhcpPackage, Section: section}); err != nil {
			return err
		}
		if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{Rollback: true, Timeout: applyTimeoutSec}); err != nil {
			return err
		}
		if err := verifyRouterHealth(ctx, client); err != nil {
			return err
		}
		readBack, err := client.UCIGet(txCtx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: section})
		if err != nil {
			return err
		}
		if readBack.SectionExists {
			return fmt.Errorf("post-delete apply read-back still found section")
		}
		_, err = client.UCIConfirm(txCtx, modernubus.UCIConfirmRequest{})
		return err
	})
}

func applyVerifyConfirm(txCtx, healthCtx context.Context, client ubusHostClient, expected ResourceModel, applyTimeoutSec int64) error {
	if _, err := client.UCIApply(txCtx, modernubus.UCIApplyRequest{Rollback: true, Timeout: applyTimeoutSec}); err != nil {
		return err
	}
	if err := verifyRouterHealth(healthCtx, client); err != nil {
		return err
	}
	live, exists, err := readLiveHost(txCtx, client, expected.Name.ValueString())
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("post-apply read-back missing section")
	}
	if !modelsEqual(live, expected) {
		return fmt.Errorf("post-apply read-back mismatch")
	}
	_, err = client.UCIConfirm(txCtx, modernubus.UCIConfirmRequest{})
	return err
}

func readLiveHost(ctx context.Context, client ubusHostClient, name string) (ResourceModel, bool, error) {
	resp, err := client.UCIGet(ctx, modernubus.UCIGetRequest{Config: dhcpPackage, Section: sectionNameForHost(name)})
	if err != nil {
		return ResourceModel{}, false, err
	}
	if !resp.SectionExists {
		return ResourceModel{}, false, nil
	}
	ip, ipOK := resp.Values["ip"].String()
	hostname, hostnameOK := resp.Values["name"].String()
	if !ipOK || !hostnameOK {
		return ResourceModel{}, false, fmt.Errorf("DHCP host section is missing required options")
	}
	model := ResourceModel{
		ID:       types.StringValue(name),
		Name:     types.StringValue(name),
		MAC:      optionalString(resp.Values, "mac"),
		IP:       types.StringValue(ip),
		DUID:     optionalString(resp.Values, "duid"),
		Hostname: types.StringNull(),
		DNS:      types.BoolValue(readDNS(resp.Values)),
	}
	if hostname != name {
		model.Hostname = types.StringValue(hostname)
	}
	return model, true, nil
}

func applyDefaults(plan *ResourceModel) {
	if plan.DNS.IsNull() || plan.DNS.IsUnknown() {
		plan.DNS = types.BoolValue(true)
	}
}

func normalizePlan(plan ResourceModel, diags *diag.Diagnostics) (ResourceModel, bool) {
	name, err := canonicalName(plan.Name.ValueString())
	if err != nil {
		diags.AddError("Invalid DHCP host name", err.Error())
	}
	ip := net.ParseIP(strings.TrimSpace(plan.IP.ValueString()))
	if ip == nil {
		diags.AddError("Invalid IP address", "ip must be a valid IPv4 or IPv6 address.")
	}
	mac := strings.TrimSpace(plan.MAC.ValueString())
	if mac != "" {
		parsed, err := net.ParseMAC(mac)
		if err != nil {
			diags.AddError("Invalid MAC address", "mac must be a valid hardware address.")
		} else {
			mac = parsed.String()
		}
	}
	hostname := ""
	if !plan.Hostname.IsNull() && !plan.Hostname.IsUnknown() {
		hostname, err = canonicalName(plan.Hostname.ValueString())
		if err != nil {
			diags.AddError("Invalid hostname", err.Error())
		}
		if hostname == name {
			hostname = ""
		}
	}
	duid := strings.ToLower(strings.TrimSpace(plan.DUID.ValueString()))
	if duid != "" && (len(duid)%2 != 0 || !validDUID.MatchString(duid)) {
		diags.AddError("Invalid DUID", "duid must contain an even number of hexadecimal characters.")
	}
	if diags.HasError() {
		return ResourceModel{}, false
	}
	normalized := ResourceModel{
		ID:       types.StringValue(name),
		Name:     types.StringValue(name),
		MAC:      nullableString(mac),
		IP:       types.StringValue(ip.String()),
		DUID:     nullableString(duid),
		Hostname: nullableString(hostname),
		DNS:      types.BoolValue(plan.DNS.ValueBool()),
	}
	return normalized, true
}

func canonicalName(value string) (string, error) {
	canonical := strings.ToLower(strings.TrimSpace(value))
	canonical = strings.TrimSuffix(canonical, ".")
	if canonical == "" {
		return "", fmt.Errorf("name cannot be empty")
	}
	if len(canonical) > maxNameLength {
		return "", fmt.Errorf("name exceeds %d characters", maxNameLength)
	}
	if !validNameChar.MatchString(canonical) {
		return "", fmt.Errorf("name contains unsupported characters")
	}
	for _, label := range strings.Split(canonical, ".") {
		if label == "" {
			return "", fmt.Errorf("name contains an empty label")
		}
	}
	return canonical, nil
}

func sectionNameForHost(name string) string {
	canonical, err := canonicalName(name)
	if err != nil {
		canonical = "invalid"
	}
	readable := strings.ReplaceAll(canonical, ".", "_")
	readable = strings.ReplaceAll(readable, "-", "_")
	readable = nonSectionChar.ReplaceAllString(readable, "_")
	readable = strings.Trim(readable, "_")
	if len(readable) > sectionReadableMax {
		readable = readable[:sectionReadableMax]
	}
	hash := sha256.Sum256([]byte(canonical))
	return sectionPrefix + readable + "_" + hex.EncodeToString(hash[:])[:sectionHashHexLen]
}

func hostValues(model ResourceModel) map[string]any {
	hostname := model.Name.ValueString()
	if !model.Hostname.IsNull() {
		hostname = model.Hostname.ValueString()
	}
	values := map[string]any{
		"name": hostname,
		"ip":   model.IP.ValueString(),
		"dns":  boolOption(model.DNS.ValueBool()),
	}
	if !model.MAC.IsNull() {
		values["mac"] = model.MAC.ValueString()
	}
	if !model.DUID.IsNull() {
		values["duid"] = model.DUID.ValueString()
	}
	return values
}

func optionalOptionsToDelete(existing modernubus.UCIGetResponse, desired ResourceModel) []string {
	var options []string
	if desired.MAC.IsNull() {
		if _, exists := existing.Values["mac"]; exists {
			options = append(options, "mac")
		}
	}
	if desired.DUID.IsNull() {
		if _, exists := existing.Values["duid"]; exists {
			options = append(options, "duid")
		}
	}
	return options
}

func optionalString(values map[string]modernubus.UCIValue, key string) types.String {
	value, ok := values[key]
	if !ok {
		return types.StringNull()
	}
	parsed, ok := value.String()
	if !ok || parsed == "" {
		return types.StringNull()
	}
	return types.StringValue(parsed)
}

func nullableString(value string) types.String {
	if value == "" {
		return types.StringNull()
	}
	return types.StringValue(value)
}

func readDNS(values map[string]modernubus.UCIValue) bool {
	value, ok := values["dns"]
	if !ok {
		return true
	}
	parsed, ok := value.String()
	return !ok || parsed != "0"
}

func boolOption(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func modelsEqual(left, right ResourceModel) bool {
	return left.ID.Equal(right.ID) &&
		left.Name.Equal(right.Name) &&
		left.MAC.Equal(right.MAC) &&
		left.IP.Equal(right.IP) &&
		left.DUID.Equal(right.DUID) &&
		left.Hostname.Equal(right.Hostname) &&
		left.DNS.Equal(right.DNS)
}

func transactionLifetime(applyTimeoutSec int64) time.Duration {
	return time.Duration(applyTimeoutSec+5) * time.Second
}

func verifyRouterHealth(ctx context.Context, client ubusHostClient) error {
	rpcURL, err := client.CurrentRPCURL()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rpcURL, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("router health check failed: http %d", resp.StatusCode)
	}
	return nil
}
