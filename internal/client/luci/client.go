package luci

import (
	"context"
	"encoding/base64"
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
	ID       string
	Name     string
	Device   string
	Proto    string
	CIDR     string
	Zone     string
	MTU      int64
	DNS      []string
	Gateway  string
	VLANID   int64
	ParentIF string
}

type UpsertDHCPHostRequest struct {
	ID       string
	Name     string
	MAC      string
	IP       string
	DUID     string
	Hostname string
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
	return strings.Join(lines, "\n")
}

func buildDHCPHostBlock(req UpsertDHCPHostRequest) string {
	lines := []string{
		"config host",
		fmt.Sprintf("\toption name '%s'", req.Name),
		fmt.Sprintf("\toption mac '%s'", req.MAC),
		fmt.Sprintf("\toption ip '%s'", req.IP),
	}
	if req.DUID != "" {
		lines = append(lines, fmt.Sprintf("\toption duid '%s'", req.DUID))
	}
	if req.Hostname != "" {
		lines = append(lines, fmt.Sprintf("\toption dns '%s'", req.Hostname))
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
	var result string
	path := "/etc/config/" + pkg
	if err := c.callRPC(ctx, "fs", "readfile", []any{path}, &result); err != nil {
		return "", err
	}

	raw, err := base64.StdEncoding.DecodeString(result)
	if err != nil {
		return "", fmt.Errorf("decoding file %s: %w", path, err)
	}
	return string(raw), nil
}

func (c *rpcClient) writeConfigFile(ctx context.Context, pkg, content string) error {
	path := "/etc/config/" + pkg
	if err := c.callRPC(ctx, "fs", "writefile", []any{path, []byte(content)}, nil); err != nil {
		return fmt.Errorf("writing %s failed: %w", path, err)
	}
	return nil
}

func (c *rpcClient) commitPackage(ctx context.Context, pkg string) error {
	var ok bool
	if err := c.callRPC(ctx, "uci", "commit", []any{pkg}, &ok); err != nil {
		return fmt.Errorf("uci commit %s failed: %w", pkg, err)
	}
	if !ok {
		return fmt.Errorf("uci commit %s returned false", pkg)
	}
	return nil
}

func (c *rpcClient) restartService(ctx context.Context, name string) error {
	var stopped bool
	if err := c.callRPC(ctx, "sys", "init.stop", []any{name}, &stopped); err != nil {
		return fmt.Errorf("stop service %s failed: %w", name, err)
	}

	var started bool
	if err := c.callRPC(ctx, "sys", "init.start", []any{name}, &started); err != nil {
		return fmt.Errorf("start service %s failed: %w", name, err)
	}
	if !started {
		return fmt.Errorf("service %s failed to start", name)
	}
	return nil
}

func (c *rpcClient) callRPC(ctx context.Context, rpcName, method string, params []any, into any) error {
	baseURL, err := url.Parse(strings.TrimRight(c.cfg.Remote, "/"))
	if err != nil {
		return fmt.Errorf("invalid remote url: %w", err)
	}

	if c.token == "" {
		if err := c.authenticate(ctx, baseURL); err != nil {
			return err
		}
	}

	doCall := func(token string) (*http.Response, error) {
		rpcURL := *baseURL
		rpcURL.Path = fmt.Sprintf("/cgi-bin/luci/rpc/%s", rpcName)
		q := rpcURL.Query()
		q.Set("auth", token)
		rpcURL.RawQuery = q.Encode()

		body, err := json.Marshal(map[string]any{
			"id":     1,
			"method": method,
			"params": params,
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
		return fmt.Errorf("rpc %s/%s returned %d: %s", rpcName, method, resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var parsed struct {
		Result json.RawMessage `json:"result"`
		Error  any             `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return err
	}
	if parsed.Error != nil {
		return fmt.Errorf("rpc %s/%s error: %v", rpcName, method, parsed.Error)
	}
	if into == nil {
		return nil
	}
	return json.Unmarshal(parsed.Result, into)
}

func (c *rpcClient) authenticate(ctx context.Context, baseURL *url.URL) error {
	authURL := *baseURL
	authURL.Path = "/cgi-bin/luci/rpc/auth"
	body, err := json.Marshal(map[string]any{
		"id":     1,
		"method": "login",
		"params": []string{c.cfg.User, c.cfg.Password},
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
		Result string `json:"result"`
		Error  any    `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return err
	}
	if parsed.Error != nil {
		return fmt.Errorf("auth rpc error: %v", parsed.Error)
	}
	if parsed.Result == "" {
		return fmt.Errorf("auth token is empty")
	}
	c.token = parsed.Result
	return nil
}
