package modernubus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const bootstrapSessionID = "00000000000000000000000000000000"

type Status int

const (
	StatusOK               Status = 0
	StatusInvalidArgument  Status = 2
	StatusNotFound         Status = 4
	StatusPermissionDenied Status = 6
	StatusUnknownError     Status = 9
)

func ParseStatus(code int) Status {
	return Status(code)
}

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusInvalidArgument:
		return "INVALID_ARGUMENT"
	case StatusNotFound:
		return "NOT_FOUND"
	case StatusPermissionDenied:
		return "PERMISSION_DENIED"
	case StatusUnknownError:
		return "UNKNOWN_ERROR"
	default:
		return fmt.Sprintf("STATUS_%d", s)
	}
}

type TransportError struct {
	Object string
	Method string
	Cause  error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("transport error calling ubus %s.%s: %v", e.Object, e.Method, e.Cause)
}

func (e *TransportError) Unwrap() error { return e.Cause }

type HTTPStatusError struct {
	Object     string
	Method     string
	StatusCode int
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("ubus %s.%s returned non-success http status %d", e.Object, e.Method, e.StatusCode)
}

type MalformedResponseError struct {
	Object string
	Method string
	Cause  error
}

func (e *MalformedResponseError) Error() string {
	return fmt.Sprintf("malformed response from ubus %s.%s: %v", e.Object, e.Method, e.Cause)
}

func (e *MalformedResponseError) Unwrap() error { return e.Cause }

type StatusError struct {
	Object string
	Method string
	Status Status
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("ubus %s.%s returned status %d (%s)", e.Object, e.Method, int(e.Status), e.Status.String())
}

type PermissionDeniedError struct {
	Object string
	Method string
	Cause  error
}

func (e *PermissionDeniedError) Error() string {
	return fmt.Sprintf("permission denied calling ubus %s.%s", e.Object, e.Method)
}

func (e *PermissionDeniedError) Unwrap() error { return e.Cause }

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

type AuthenticationError struct {
	Cause error
}

func (e *AuthenticationError) Error() string {
	return fmt.Sprintf("authentication failed: %v", e.Cause)
}

func (e *AuthenticationError) Unwrap() error { return e.Cause }

type Config struct {
	Remote      string
	User        string
	Password    string
	HTTPClient  *http.Client
	Now         func() time.Time
	SessionSkew time.Duration
}

type ValidationError struct {
	Field string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid request: missing required field %q", e.Field)
}

type UCIGetRequest struct {
	Config  string
	Section string
	Option  string
	Type    string
	Match   map[string]any
}

type UCIValue struct {
	raw any
}

func (v UCIValue) Raw() any {
	return v.raw
}

func (v UCIValue) String() (string, bool) {
	s, ok := v.raw.(string)
	return s, ok
}

func (v UCIValue) List() ([]string, bool) {
	listAny, ok := v.raw.([]any)
	if !ok {
		return nil, false
	}
	list := make([]string, 0, len(listAny))
	for _, item := range listAny {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		list = append(list, s)
	}
	return list, true
}

type UCIGetResponse struct {
	Package        string
	Section        string
	Option         string
	PackageExists  bool
	SectionExists  bool
	OptionExists   bool
	EmptyPackage   bool
	HasValues      bool
	Values         map[string]UCIValue
	MetadataName   string
	MetadataType   string
	MetadataIsAnon *bool
}

type UCIAddRequest struct {
	Config string
	Type   string
	Name   string
	Values map[string]any
}

type UCIAddResponse struct {
	Section string
}

type UCIChangesRequest struct {
	Config string
}

type UCIChange struct {
	Items []UCIValue
}

func (c UCIChange) StringAt(index int) (string, bool) {
	if index < 0 || index >= len(c.Items) {
		return "", false
	}
	return c.Items[index].String()
}

type UCIChangesResponse struct {
	Changes []UCIChange
}

type UCIRevertRequest struct {
	Config string
}

type UCIRevertResponse struct{}

type Client struct {
	remote      string
	user        string
	password    string
	httpClient  *http.Client
	now         func() time.Time
	sessionSkew time.Duration

	mu        sync.Mutex
	token     string
	timeout   int64
	expiresAt time.Time
}

func NewClient(cfg Config) *Client {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	sessionSkew := cfg.SessionSkew
	if sessionSkew == 0 {
		sessionSkew = 2 * time.Second
	}
	return &Client{
		remote:      strings.TrimRight(cfg.Remote, "/"),
		user:        cfg.User,
		password:    cfg.Password,
		httpClient:  httpClient,
		now:         now,
		sessionSkew: sessionSkew,
	}
}

func (c *Client) Call(ctx context.Context, object, method string, args map[string]any, into any) error {
	if err := c.ensureSession(ctx); err != nil {
		return err
	}

	c.mu.Lock()
	token := c.token
	c.mu.Unlock()

	resp, err := c.rpcCall(ctx, token, object, method, args)
	if err != nil {
		return &TransportError{Object: object, Method: method, Cause: err}
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		c.mu.Lock()
		c.token = ""
		c.timeout = 0
		c.expiresAt = time.Time{}
		c.mu.Unlock()

		if err := c.forceReauthenticate(ctx); err != nil {
			return err
		}
		c.mu.Lock()
		token = c.token
		c.mu.Unlock()

		resp, err = c.rpcCall(ctx, token, object, method, args)
		if err != nil {
			return &TransportError{Object: object, Method: method, Cause: err}
		}
		defer resp.Body.Close()
	}

	return decodeCallResponse(resp, object, method, into)
}

func (c *Client) UCIGet(ctx context.Context, req UCIGetRequest) (UCIGetResponse, error) {
	if strings.TrimSpace(req.Config) == "" {
		return UCIGetResponse{}, &ValidationError{Field: "config"}
	}

	args := map[string]any{
		"config": req.Config,
	}
	if req.Section != "" {
		args["section"] = req.Section
	}
	if req.Option != "" {
		args["option"] = req.Option
	}
	if req.Type != "" {
		args["type"] = req.Type
	}
	if len(req.Match) > 0 {
		args["match"] = req.Match
	}

	var payload struct {
		Values any `json:"values"`
	}
	err := c.Call(ctx, "uci", "get", args, &payload)
	if err != nil {
		return UCIGetResponse{}, err
	}

	out := UCIGetResponse{
		Package:       req.Config,
		Section:       req.Section,
		Option:        req.Option,
		PackageExists: true,
		SectionExists: req.Section == "",
		OptionExists:  req.Option == "",
		Values:        map[string]UCIValue{},
	}

	if payload.Values == nil {
		// Firmware behavior: result [0] with no payload for missing section/option.
		return out, nil
	}

	out.HasValues = true
	switch raw := payload.Values.(type) {
	case []any:
		if req.Section == "" {
			out.EmptyPackage = len(raw) == 0
			out.SectionExists = true
		}
		return out, nil
	case map[string]any:
		out.SectionExists = true
		if req.Option != "" {
			_, ok := raw[req.Option]
			out.OptionExists = ok
		}
		for k, v := range raw {
			out.Values[k] = UCIValue{raw: v}
			switch k {
			case ".name":
				if s, ok := v.(string); ok {
					out.MetadataName = s
				}
			case ".type":
				if s, ok := v.(string); ok {
					out.MetadataType = s
				}
			case ".anonymous":
				if b, ok := v.(bool); ok {
					val := b
					out.MetadataIsAnon = &val
				}
			}
		}
		return out, nil
	default:
		return UCIGetResponse{}, &MalformedResponseError{
			Object: "uci",
			Method: "get",
			Cause:  fmt.Errorf("unexpected values type %T", raw),
		}
	}
}

func (c *Client) UCIAdd(ctx context.Context, req UCIAddRequest) (UCIAddResponse, error) {
	if strings.TrimSpace(req.Config) == "" {
		return UCIAddResponse{}, &ValidationError{Field: "config"}
	}
	if strings.TrimSpace(req.Type) == "" {
		return UCIAddResponse{}, &ValidationError{Field: "type"}
	}

	args := map[string]any{
		"config": req.Config,
		"type":   req.Type,
	}
	if strings.TrimSpace(req.Name) != "" {
		args["name"] = req.Name
	}
	if req.Values != nil {
		args["values"] = req.Values
	}

	var payload struct {
		Section string `json:"section"`
	}
	if err := c.Call(ctx, "uci", "add", args, &payload); err != nil {
		return UCIAddResponse{}, err
	}
	if strings.TrimSpace(payload.Section) == "" {
		return UCIAddResponse{}, &MalformedResponseError{
			Object: "uci",
			Method: "add",
			Cause:  fmt.Errorf("missing section in response"),
		}
	}
	return UCIAddResponse{Section: payload.Section}, nil
}

func (c *Client) UCIChanges(ctx context.Context, req UCIChangesRequest) (UCIChangesResponse, error) {
	if strings.TrimSpace(req.Config) == "" {
		return UCIChangesResponse{}, &ValidationError{Field: "config"}
	}

	args := map[string]any{"config": req.Config}
	var payload struct {
		Changes any `json:"changes"`
	}
	if err := c.Call(ctx, "uci", "changes", args, &payload); err != nil {
		return UCIChangesResponse{}, err
	}

	if payload.Changes == nil {
		return UCIChangesResponse{Changes: []UCIChange{}}, nil
	}
	changeRows, ok := payload.Changes.([]any)
	if !ok {
		return UCIChangesResponse{}, &MalformedResponseError{
			Object: "uci",
			Method: "changes",
			Cause:  fmt.Errorf("unexpected changes type %T", payload.Changes),
		}
	}

	parsed := make([]UCIChange, 0, len(changeRows))
	for _, row := range changeRows {
		items, ok := row.([]any)
		if !ok {
			return UCIChangesResponse{}, &MalformedResponseError{
				Object: "uci",
				Method: "changes",
				Cause:  fmt.Errorf("unexpected change row type %T", row),
			}
		}
		change := UCIChange{Items: make([]UCIValue, 0, len(items))}
		for _, item := range items {
			change.Items = append(change.Items, UCIValue{raw: item})
		}
		parsed = append(parsed, change)
	}
	return UCIChangesResponse{Changes: parsed}, nil
}

func (c *Client) UCIRevert(ctx context.Context, req UCIRevertRequest) (UCIRevertResponse, error) {
	if strings.TrimSpace(req.Config) == "" {
		return UCIRevertResponse{}, &ValidationError{Field: "config"}
	}
	if err := c.Call(ctx, "uci", "revert", map[string]any{"config": req.Config}, nil); err != nil {
		return UCIRevertResponse{}, err
	}
	return UCIRevertResponse{}, nil
}

type SessionInfo struct {
	HasSession bool
	TimeoutSec int64
	ExpiresAt  time.Time
}

func (c *Client) SessionInfo() SessionInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return SessionInfo{
		HasSession: c.token != "",
		TimeoutSec: c.timeout,
		ExpiresAt:  c.expiresAt,
	}
}

func (c *Client) ensureSession(ctx context.Context) error {
	c.mu.Lock()
	token := c.token
	expiresAt := c.expiresAt
	c.mu.Unlock()

	if token == "" {
		return c.forceReauthenticate(ctx)
	}
	if !expiresAt.IsZero() && c.now().Add(c.sessionSkew).After(expiresAt) {
		return c.forceReauthenticate(ctx)
	}
	return nil
}

func (c *Client) forceReauthenticate(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && (c.expiresAt.IsZero() || c.now().Add(c.sessionSkew).Before(c.expiresAt)) {
		return nil
	}

	authURL, err := c.rpcURL()
	if err != nil {
		return err
	}

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "call",
		"params": []any{
			bootstrapSessionID,
			"session",
			"login",
			map[string]any{
				"username": c.user,
				"password": c.password,
			},
		},
	})
	if err != nil {
		return &AuthenticationError{Cause: err}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, bytes.NewReader(body))
	if err != nil {
		return &AuthenticationError{Cause: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return &AuthenticationError{Cause: err}
	}
	defer resp.Body.Close()

	var parsed struct {
		Result []json.RawMessage `json:"result"`
		Error  *RPCError         `json:"error"`
	}

	if resp.StatusCode != http.StatusOK {
		return &AuthenticationError{Cause: &HTTPStatusError{
			Object:     "session",
			Method:     "login",
			StatusCode: resp.StatusCode,
		}}
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return &AuthenticationError{Cause: &MalformedResponseError{
			Object: "session",
			Method: "login",
			Cause:  err,
		}}
	}
	if parsed.Error != nil {
		if parsed.Error.Code == -32002 {
			return &AuthenticationError{Cause: &PermissionDeniedError{
				Object: "session",
				Method: "login",
				Cause:  parsed.Error,
			}}
		}
		return &AuthenticationError{Cause: parsed.Error}
	}
	if len(parsed.Result) < 2 {
		return &AuthenticationError{Cause: &MalformedResponseError{
			Object: "session",
			Method: "login",
			Cause:  fmt.Errorf("session.login returned incomplete result"),
		}}
	}

	var code int
	if err := json.Unmarshal(parsed.Result[0], &code); err != nil {
		return &AuthenticationError{Cause: &MalformedResponseError{
			Object: "session",
			Method: "login",
			Cause:  err,
		}}
	}
	if Status(code) != StatusOK {
		statusErr := &StatusError{Object: "session", Method: "login", Status: Status(code)}
		if statusErr.Status == StatusPermissionDenied {
			return &AuthenticationError{Cause: &PermissionDeniedError{
				Object: "session",
				Method: "login",
				Cause:  statusErr,
			}}
		}
		return &AuthenticationError{Cause: statusErr}
	}

	var auth struct {
		UBUSRPCSession string `json:"ubus_rpc_session"`
		Timeout        int64  `json:"timeout"`
	}
	if err := json.Unmarshal(parsed.Result[1], &auth); err != nil {
		return &AuthenticationError{Cause: &MalformedResponseError{
			Object: "session",
			Method: "login",
			Cause:  err,
		}}
	}
	if auth.UBUSRPCSession == "" {
		return &AuthenticationError{Cause: &MalformedResponseError{
			Object: "session",
			Method: "login",
			Cause:  fmt.Errorf("session.login returned empty session"),
		}}
	}

	c.token = auth.UBUSRPCSession
	c.timeout = auth.Timeout
	if auth.Timeout > 0 {
		c.expiresAt = c.now().Add(time.Duration(auth.Timeout) * time.Second)
	} else {
		c.expiresAt = time.Time{}
	}

	return nil
}

func (c *Client) rpcCall(ctx context.Context, sessionID, object, method string, args map[string]any) (*http.Response, error) {
	rpcURL, err := c.rpcURL()
	if err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "call",
		"params":  []any{sessionID, object, method, args},
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, rpcURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	return c.httpClient.Do(httpReq)
}

func (c *Client) rpcURL() (string, error) {
	baseURL, err := url.Parse(c.remote)
	if err != nil {
		return "", fmt.Errorf("invalid remote url: %w", err)
	}
	baseURL.Path = "/cgi-bin/luci/admin/ubus"
	return baseURL.String(), nil
}

func decodeCallResponse(resp *http.Response, object, method string, into any) error {
	if resp.StatusCode != http.StatusOK {
		return &HTTPStatusError{
			Object:     object,
			Method:     method,
			StatusCode: resp.StatusCode,
		}
	}

	var parsed struct {
		Result []json.RawMessage `json:"result"`
		Error  *RPCError         `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return &MalformedResponseError{
			Object: object,
			Method: method,
			Cause:  err,
		}
	}
	if parsed.Error != nil {
		if parsed.Error.Code == -32002 {
			return &PermissionDeniedError{
				Object: object,
				Method: method,
				Cause:  parsed.Error,
			}
		}
		return parsed.Error
	}
	if len(parsed.Result) == 0 {
		return &MalformedResponseError{
			Object: object,
			Method: method,
			Cause:  fmt.Errorf("ubus call returned no result"),
		}
	}

	var code int
	if err := json.Unmarshal(parsed.Result[0], &code); err != nil {
		return &MalformedResponseError{
			Object: object,
			Method: method,
			Cause:  err,
		}
	}
	status := Status(code)
	if status != StatusOK {
		statusErr := &StatusError{
			Object: object,
			Method: method,
			Status: status,
		}
		if status == StatusPermissionDenied {
			return &PermissionDeniedError{
				Object: object,
				Method: method,
				Cause:  statusErr,
			}
		}
		return statusErr
	}
	if into == nil {
		return nil
	}
	if len(parsed.Result) < 2 {
		return nil
	}
	if err := json.Unmarshal(parsed.Result[1], into); err != nil {
		return &MalformedResponseError{
			Object: object,
			Method: method,
			Cause:  err,
		}
	}
	return nil
}

func IsAuthenticationError(err error) bool {
	var target *AuthenticationError
	return errors.As(err, &target)
}

func IsPermissionDenied(err error) bool {
	var target *PermissionDeniedError
	return errors.As(err, &target)
}
