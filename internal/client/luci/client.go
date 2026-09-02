package luci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	Remote   string
	User     string
	Password string
}

type Client interface {
	ApplyNetwork(ctx context.Context, req ApplyNetworkRequest) error
	DeleteNetwork(ctx context.Context, name string) error
	UpsertDHCPHost(ctx context.Context, req UpsertDHCPHostRequest) error
	DeleteDHCPHost(ctx context.Context, id string) error
	ApplyManagedBlocks(ctx context.Context, blocks []ManagedBlock) error
	DeleteManagedBlocks(ctx context.Context, blocks []ManagedBlock) error
}

type ApplyNetworkRequest struct {
	ID        string
	Name      string
	Device    string
	Proto     string
	CIDR      string
	Zone      string
	MTU       int64
	DNS       []string
	Gateway   string
	VLANID    int64
	ParentIF  string
	IP6Assign int64
	Delegate  *bool
}

type UpsertDHCPHostRequest struct {
	ID       string
	Name     string
	MAC      string
	IP       string
	DUID     string
	Hostname string
	DNS      bool
}

type ManagedBlock struct {
	Package string
	Service string
	Key     string
	Block   string
}

type rpcClient struct {
	cfg        Config
	httpClient *http.Client
	token      string
}

const ubusSessionID = "00000000000000000000000000000000"

func NewClient(cfg Config) Client {
	return &rpcClient{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: 25 * time.Second,
		},
	}
}

func (c *rpcClient) ApplyNetwork(ctx context.Context, req ApplyNetworkRequest) error {
	return c.ApplyManagedBlocks(ctx, []ManagedBlock{{
		Package: "network",
		Service: "network",
		Key:     markerKey("network", req.Name),
		Block:   buildNetworkBlock(req),
	}})
}

func (c *rpcClient) DeleteNetwork(ctx context.Context, name string) error {
	return c.DeleteManagedBlocks(ctx, []ManagedBlock{{
		Package: "network",
		Service: "network",
		Key:     markerKey("network", name),
	}})
}

func (c *rpcClient) UpsertDHCPHost(ctx context.Context, req UpsertDHCPHostRequest) error {
	return c.ApplyManagedBlocks(ctx, []ManagedBlock{{
		Package: "dhcp",
		Service: "dnsmasq",
		Key:     markerKey("dhcp-host", req.Name),
		Block:   buildDHCPHostBlock(req),
	}})
}

func (c *rpcClient) DeleteDHCPHost(ctx context.Context, name string) error {
	return c.DeleteManagedBlocks(ctx, []ManagedBlock{{
		Package: "dhcp",
		Service: "dnsmasq",
		Key:     markerKey("dhcp-host", name),
	}})
}

func markerKey(kind, name string) string {
	return fmt.Sprintf("%s:%s", kind, name)
}

func markerLines(key string) (string, string) {
	return "# BEGIN terraform-provider-openwrt " + key, "# END terraform-provider-openwrt " + key
}

func upsertManagedBlock(content, key, block string) string {
	start, end := markerLines(key)
	newBlock := start + "\n" + strings.TrimSpace(block) + "\n" + end + "\n"

	if strings.Contains(content, start) && strings.Contains(content, end) {
		before, _, _, ok := splitManagedBlock(content, start, end)
		if ok {
			_, _, after, _ := splitManagedBlock(content, start, end)
			return strings.TrimRight(before, "\n") + "\n\n" + newBlock + strings.TrimLeft(after, "\n")
		}
	}

	if strings.TrimSpace(content) == "" {
		return newBlock
	}
	return strings.TrimRight(content, "\n") + "\n\n" + newBlock
}

func removeManagedBlock(content, key string) string {
	start, end := markerLines(key)
	before, _, after, ok := splitManagedBlock(content, start, end)
	if !ok {
		return content
	}
	joined := strings.TrimRight(before, "\n")
	if strings.TrimSpace(after) != "" {
		if joined != "" {
			joined += "\n\n"
		}
		joined += strings.TrimLeft(after, "\n")
	}
	return strings.TrimRight(joined, "\n") + "\n"
}

func splitManagedBlock(content, start, end string) (before, managed, after string, ok bool) {
	si := strings.Index(content, start)
	if si < 0 {
		return "", "", "", false
	}
	ei := strings.Index(content[si:], end)
	if ei < 0 {
		return "", "", "", false
	}
	ei = si + ei + len(end)
	if ei < len(content) && content[ei] == '\n' {
		ei++
	}
	return content[:si], content[si:ei], content[ei:], true
}

func buildNetworkBlock(req ApplyNetworkRequest) string {
	lines := []string{
		fmt.Sprintf("config interface '%s'", req.Name),
		fmt.Sprintf("\toption device '%s'", req.Device),
		fmt.Sprintf("\toption proto '%s'", req.Proto),
	}

	if req.CIDR != "" {
		ip, ipNet, err := net.ParseCIDR(req.CIDR)
		if err == nil {
			lines = append(lines,
				fmt.Sprintf("\toption ipaddr '%s'", ip.String()),
				fmt.Sprintf("\toption netmask '%s'", net.IP(ipNet.Mask).String()),
			)
		}
	}

	if req.Gateway != "" {
		lines = append(lines, fmt.Sprintf("\toption gateway '%s'", req.Gateway))
	}
	for _, d := range req.DNS {
		if strings.TrimSpace(d) != "" {
			lines = append(lines, fmt.Sprintf("\tlist dns '%s'", d))
		}
	}
	if req.MTU > 0 {
		lines = append(lines, fmt.Sprintf("\toption mtu '%d'", req.MTU))
	}
	if req.IP6Assign > 0 {
		lines = append(lines, fmt.Sprintf("\toption ip6assign '%d'", req.IP6Assign))
	}
	if req.Delegate != nil {
		if *req.Delegate {
			lines = append(lines, "\toption delegate '1'")
		} else {
			lines = append(lines, "\toption delegate '0'")
		}
	}
	return strings.Join(lines, "\n")
}

func buildDHCPHostBlock(req UpsertDHCPHostRequest) string {
	recordName := req.Name
	if req.Hostname != "" {
		recordName = req.Hostname
	}
	lines := []string{
		"config host",
		fmt.Sprintf("\toption name '%s'", recordName),
		fmt.Sprintf("\toption mac '%s'", req.MAC),
		fmt.Sprintf("\toption ip '%s'", req.IP),
	}
	if req.DUID != "" {
		lines = append(lines, fmt.Sprintf("\toption duid '%s'", req.DUID))
	}
	if req.DNS {
		lines = append(lines, "\toption dns '1'")
	} else {
		lines = append(lines, "\toption dns '0'")
	}
	return strings.Join(lines, "\n")
}

func (c *rpcClient) rollbackFile(ctx context.Context, pkg, content string) error {
	if err := c.writeConfigFile(ctx, pkg, content); err != nil {
		return err
	}
	return c.commitPackage(ctx, pkg)
}

func (c *rpcClient) readConfigFile(ctx context.Context, pkg string) (string, error) {
	path := "/etc/config/" + pkg
	var result struct {
		Data string `json:"data"`
	}
	if err := c.callUBUS(ctx, "file", "read", map[string]any{"path": path}, &result); err != nil {
		return "", err
	}
	return result.Data, nil
}

func (c *rpcClient) writeConfigFile(ctx context.Context, pkg, content string) error {
	path := "/etc/config/" + pkg
	if err := c.callUBUS(ctx, "file", "write", map[string]any{
		"path": path,
		"data": content,
	}, nil); err != nil {
		return fmt.Errorf("writing %s failed: %w", path, err)
	}
	return nil
}

func (c *rpcClient) commitPackage(ctx context.Context, pkg string) error {
	if err := c.callUBUS(ctx, "uci", "commit", map[string]any{"config": pkg}, nil); err != nil {
		return fmt.Errorf("uci commit %s failed: %w", pkg, err)
	}
	return nil
}

func (c *rpcClient) restartService(ctx context.Context, name string) error {
	restartResult := struct {
		Code   int    `json:"code"`
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}{}
	if err := c.callUBUS(ctx, "file", "exec", map[string]any{
		"command": "/etc/init.d/" + name,
		"params":  []string{"restart"},
	}, &restartResult); err != nil {
		return fmt.Errorf("restart service %s failed: %w", name, err)
	}
	if restartResult.Code != 0 {
		return fmt.Errorf(
			"service %s restart exited with code %d (stdout=%q stderr=%q)",
			name,
			restartResult.Code,
			strings.TrimSpace(restartResult.Stdout),
			strings.TrimSpace(restartResult.Stderr),
		)
	}
	return nil
}

func (c *rpcClient) callUBUS(ctx context.Context, object, method string, args map[string]any, into any) error {
	baseURL, err := url.Parse(strings.TrimRight(c.cfg.Remote, "/"))
	if err != nil {
		return fmt.Errorf("invalid remote url: %w", err)
	}

	if c.token == "" {
		if err := c.authenticate(ctx, baseURL); err != nil {
			return err
		}
	}

	if args == nil {
		args = map[string]any{}
	}

	doCall := func(token string) (*http.Response, error) {
		rpcURL := *baseURL
		rpcURL.Path = "/cgi-bin/luci/admin/ubus"

		body, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "call",
			"params":  []any{token, object, method, args},
		})
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL.String(), strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return c.httpClient.Do(req)
	}

	resp, err := doCall(c.token)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		c.token = ""
		if err := c.authenticate(ctx, baseURL); err != nil {
			return err
		}
		resp, err = doCall(c.token)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
	}

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ubus %s.%s returned %d: %s", object, method, resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var parsed struct {
		Result []json.RawMessage `json:"result"`
		Error  any               `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return err
	}
	if parsed.Error != nil {
		return fmt.Errorf("ubus %s.%s error: %v", object, method, parsed.Error)
	}
	if len(parsed.Result) == 0 {
		return fmt.Errorf("ubus %s.%s returned no result payload", object, method)
	}

	var ubusCode int
	if err := json.Unmarshal(parsed.Result[0], &ubusCode); err != nil {
		return fmt.Errorf("ubus %s.%s returned invalid status code: %w", object, method, err)
	}
	if ubusCode != 0 {
		return fmt.Errorf("ubus %s.%s returned status %d", object, method, ubusCode)
	}
	if into == nil {
		return nil
	}
	if len(parsed.Result) < 2 {
		return fmt.Errorf("ubus %s.%s returned no response object", object, method)
	}
	return json.Unmarshal(parsed.Result[1], into)
}

func (c *rpcClient) authenticate(ctx context.Context, baseURL *url.URL) error {
	authURL := *baseURL
	authURL.Path = "/cgi-bin/luci/admin/ubus"
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "call",
		"params": []any{
			ubusSessionID,
			"session",
			"login",
			map[string]any{
				"username": c.cfg.User,
				"password": c.cfg.Password,
			},
		},
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL.String(), strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("auth failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var parsed struct {
		Result []json.RawMessage `json:"result"`
		Error  any               `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return err
	}
	if parsed.Error != nil {
		return fmt.Errorf("auth ubus error: %v", parsed.Error)
	}
	if len(parsed.Result) < 2 {
		return fmt.Errorf("auth result is incomplete")
	}

	var ubusCode int
	if err := json.Unmarshal(parsed.Result[0], &ubusCode); err != nil {
		return fmt.Errorf("invalid auth status code: %w", err)
	}
	if ubusCode != 0 {
		return fmt.Errorf("auth failed with ubus status %d", ubusCode)
	}

	var authResult struct {
		Token string `json:"ubus_rpc_session"`
	}
	if err := json.Unmarshal(parsed.Result[1], &authResult); err != nil {
		return fmt.Errorf("invalid auth payload: %w", err)
	}
	if authResult.Token == "" {
		return fmt.Errorf("auth token is empty")
	}
	c.token = authResult.Token
	return nil
}
