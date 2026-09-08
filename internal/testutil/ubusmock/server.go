package ubusmock

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	RoutePath           = "/cgi-bin/luci/admin/ubus"
	bootstrapSessionID  = "00000000000000000000000000000000"
	sentinelSectionName = "sentinel_static"
)

type FailureKind string

const (
	FailureTransport FailureKind = "transport"
	FailureHTTP      FailureKind = "http_status"
	FailureJSONRPC   FailureKind = "jsonrpc"
	FailureUBus      FailureKind = "ubus_status"
	FailureMalformed FailureKind = "malformed"
)

type OneShotFailure struct {
	Kind       FailureKind
	HTTPStatus int
	RPCCode    int
	RPCMessage string
	UbusStatus int
}

type RequestRecord struct {
	Sequence int
	Session  string
	Object   string
	Method   string
	Args     map[string]any
}

type SessionInfo struct {
	Token     string
	ExpiresAt time.Time
}

type section struct {
	Type      string
	Anonymous bool
	Index     int
	Values    map[string]any
}

type stagedState struct {
	sections map[string]map[string]section
	deleted  map[string]map[string]bool
	changes  map[string][][]any
}

type pendingApply struct {
	sessionToken string
	dueAt        time.Time
	snapshot     map[string]map[string]section
}

type mockSession struct {
	token     string
	expiresAt time.Time
	staged    stagedState
}

type Server struct {
	server         *httptest.Server
	sessionTimeout time.Duration
	now            func() time.Time

	mu             sync.Mutex
	seq            int
	nextAnonNumber int
	nextIndex      int
	sessions       map[string]*mockSession
	committed      map[string]map[string]section
	pendingApply   *pendingApply
	records        []RequestRecord
	failures       map[string][]OneShotFailure
}

func NewServer() *Server {
	s := &Server{
		sessionTimeout: 45 * time.Second,
		now:            time.Now,
		nextAnonNumber: 1,
		nextIndex:      1,
		sessions:       map[string]*mockSession{},
		committed: map[string]map[string]section{
			"dhcp": {
				sentinelSectionName: {
					Type:      "domain",
					Anonymous: false,
					Index:     0,
					Values: map[string]any{
						"name": "sentinel.invalid",
						"ip":   "203.0.113.99",
					},
				},
			},
		},
		failures: map[string][]OneShotFailure{},
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *Server) Close() {
	s.server.Close()
}

func (s *Server) URL() string {
	return s.server.URL
}

func IsLoopbackURL(raw string) bool {
	parts := strings.Split(strings.TrimPrefix(raw, "http://"), "/")
	hostPort := parts[0]
	host := hostPort
	if strings.Contains(hostPort, ":") {
		if strings.HasPrefix(hostPort, "[") {
			host = strings.TrimPrefix(strings.Split(hostPort, "]")[0], "[")
		} else {
			host = strings.Split(hostPort, ":")[0]
		}
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) RequestHistory() []RequestRecord {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]RequestRecord, 0, len(s.records))
	for _, record := range s.records {
		out = append(out, RequestRecord{
			Sequence: record.Sequence,
			Session:  record.Session,
			Object:   record.Object,
			Method:   record.Method,
			Args:     deepCopyMap(record.Args),
		})
	}
	return out
}

func (s *Server) SetOneShotFailure(object, method string, failure OneShotFailure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := failureKey(object, method)
	s.failures[key] = append(s.failures[key], failure)
}

func (s *Server) ExpireAllSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, session := range s.sessions {
		session.expiresAt = s.now().Add(-1 * time.Second)
	}
}

func (s *Server) TriggerRollbackNow() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingApply != nil {
		s.pendingApply.dueAt = s.now().Add(-1 * time.Second)
	}
	s.applyPendingRollbackLocked()
}

func (s *Server) InjectOutOfBandOption(config, sectionName, option string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	section, err := s.committedSectionLocked(config, sectionName)
	if err != nil {
		return err
	}
	section.Values[option] = value
	s.setCommittedSectionLocked(config, sectionName, section)
	return nil
}

func (s *Server) DeleteCommittedSection(config, sectionName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if config == "dhcp" && sectionName == sentinelSectionName {
		return errors.New("cannot delete sentinel section")
	}
	pkg, ok := s.committed[config]
	if !ok {
		return fmt.Errorf("config %q not found", config)
	}
	delete(pkg, sectionName)
	return nil
}

func (s *Server) Section(config, sectionName string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pkg, ok := s.committed[config]
	if !ok {
		return nil, false
	}
	entry, ok := pkg[sectionName]
	if !ok {
		return nil, false
	}
	values := map[string]any{
		".name":      sectionName,
		".type":      entry.Type,
		".anonymous": entry.Anonymous,
		".index":     entry.Index,
	}
	for key, value := range entry.Values {
		values[key] = value
	}
	return values, true
}

func (s *Server) PackageSnapshot(config string) map[string]map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	pkg, ok := s.committed[config]
	if !ok {
		return map[string]map[string]any{}
	}
	out := make(map[string]map[string]any, len(pkg))
	for sectionName, entry := range pkg {
		values := map[string]any{
			".name":      sectionName,
			".type":      entry.Type,
			".anonymous": entry.Anonymous,
			".index":     entry.Index,
		}
		for key, value := range entry.Values {
			values[key] = value
		}
		out[sectionName] = values
	}
	return out
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != RoutePath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.mu.Lock()
	s.applyPendingRollbackLocked()
	s.mu.Unlock()

	if r.Method == http.MethodGet {
		w.WriteHeader(http.StatusOK)
		return
	}

	var rpcReq struct {
		ID      any    `json:"id"`
		Method  string `json:"method"`
		JSONRPC string `json:"jsonrpc"`
		Params  []any  `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	if rpcReq.Method != "call" || len(rpcReq.Params) != 4 {
		s.writeRPCError(w, rpcReq.ID, -32601, "method not found")
		return
	}

	session, _ := rpcReq.Params[0].(string)
	object, _ := rpcReq.Params[1].(string)
	method, _ := rpcReq.Params[2].(string)
	args, _ := rpcReq.Params[3].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}

	if !isAllowedMethod(object, method) {
		s.writeRPCError(w, rpcReq.ID, -32601, "unsupported method")
		return
	}

	s.mu.Lock()
	s.seq++
	s.records = append(s.records, RequestRecord{
		Sequence: s.seq,
		Session:  session,
		Object:   object,
		Method:   method,
		Args:     deepCopyMap(args),
	})
	failure := s.popFailureLocked(object, method)
	s.mu.Unlock()

	if failure != nil {
		switch failure.Kind {
		case FailureTransport:
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				http.Error(w, "transport failure", http.StatusServiceUnavailable)
				return
			}
			conn, _, err := hijacker.Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		case FailureHTTP:
			status := failure.HTTPStatus
			if status == 0 {
				status = http.StatusForbidden
			}
			http.Error(w, "forced http error", status)
			return
		case FailureJSONRPC:
			code := failure.RPCCode
			if code == 0 {
				code = -32002
			}
			msg := failure.RPCMessage
			if msg == "" {
				msg = "Access denied"
			}
			s.writeRPCError(w, rpcReq.ID, code, msg)
			return
		case FailureUBus:
			status := failure.UbusStatus
			if status == 0 {
				status = 9
			}
			s.writeRPCResult(w, rpcReq.ID, []any{status})
			return
		case FailureMalformed:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,`))
			return
		}
	}

	if object == "session" && method == "login" {
		s.handleLogin(w, rpcReq.ID, session, args)
		return
	}

	if err := s.validateSession(session); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if object == "uci" {
		s.handleUCI(w, rpcReq.ID, session, method, args)
		return
	}

	s.writeRPCError(w, rpcReq.ID, -32601, "method not found")
}

func (s *Server) handleLogin(w http.ResponseWriter, id any, sessionToken string, args map[string]any) {
	if sessionToken != bootstrapSessionID {
		s.writeRPCResult(w, id, []any{6})
		return
	}
	user, _ := args["username"].(string)
	pass, _ := args["password"].(string)
	if user == "" || pass == "" {
		s.writeRPCResult(w, id, []any{6})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	token := fmt.Sprintf("sess-%08x", rand.Uint32())
	s.sessions[token] = &mockSession{
		token:     token,
		expiresAt: s.now().Add(s.sessionTimeout),
		staged: stagedState{
			sections: map[string]map[string]section{},
			deleted:  map[string]map[string]bool{},
			changes:  map[string][][]any{},
		},
	}

	payload := map[string]any{
		"ubus_rpc_session": token,
		"timeout":          int64(s.sessionTimeout / time.Second),
	}
	s.writeRPCResult(w, id, []any{0, payload})
}

func (s *Server) validateSession(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[token]
	if !ok {
		return errors.New("missing session")
	}
	if s.now().After(session.expiresAt) {
		delete(s.sessions, token)
		return errors.New("expired session")
	}
	return nil
}

func (s *Server) handleUCI(w http.ResponseWriter, id any, token, method string, args map[string]any) {
	switch method {
	case "get":
		s.handleUCIGet(w, id, token, args)
	case "add":
		s.handleUCIAdd(w, id, token, args)
	case "set":
		s.handleUCISet(w, id, token, args)
	case "delete":
		s.handleUCIDelete(w, id, token, args)
	case "changes":
		s.handleUCIChanges(w, id, token, args)
	case "revert":
		s.handleUCIRevert(w, id, token, args)
	case "commit":
		s.handleUCICommit(w, id, token, args)
	case "apply":
		s.handleUCIApply(w, id, token, args)
	case "confirm":
		s.handleUCIConfirm(w, id, token)
	default:
		s.writeRPCError(w, id, -32601, "method not found")
	}
}

func (s *Server) handleUCIGet(w http.ResponseWriter, id any, token string, args map[string]any) {
	config, _ := args["config"].(string)
	sectionName, _ := args["section"].(string)
	option, _ := args["option"].(string)
	if config == "" {
		s.writeRPCResult(w, id, []any{2})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	view := s.sessionViewLocked(token, config)

	if sectionName == "" {
		if len(view) == 0 {
			s.writeRPCResult(w, id, []any{0, map[string]any{"values": map[string]any{}}})
			return
		}
		values := map[string]any{}
		sections := make([]string, 0, len(view))
		for name := range view {
			sections = append(sections, name)
		}
		slices.Sort(sections)
		for _, name := range sections {
			values[name] = sectionToValues(name, view[name])
		}
		s.writeRPCResult(w, id, []any{0, map[string]any{"values": values}})
		return
	}

	entry, ok := view[sectionName]
	if !ok {
		s.writeRPCResult(w, id, []any{0})
		return
	}
	values := sectionToValues(sectionName, entry)
	if option != "" {
		value, ok := values[option]
		if !ok {
			s.writeRPCResult(w, id, []any{0})
			return
		}
		s.writeRPCResult(w, id, []any{0, map[string]any{"values": map[string]any{option: value}}})
		return
	}
	s.writeRPCResult(w, id, []any{0, map[string]any{"values": values}})
}

func (s *Server) handleUCIAdd(w http.ResponseWriter, id any, token string, args map[string]any) {
	config, _ := args["config"].(string)
	typ, _ := args["type"].(string)
	sectionName, _ := args["name"].(string)
	values, _ := args["values"].(map[string]any)
	if config == "" || typ == "" {
		s.writeRPCResult(w, id, []any{2})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[token]
	view := s.sessionViewLocked(token, config)

	if sectionName == "" {
		sectionName = fmt.Sprintf("cfg%04d", s.nextAnonNumber)
		s.nextAnonNumber++
	}
	if _, exists := view[sectionName]; exists {
		s.writeRPCResult(w, id, []any{2})
		return
	}

	s.ensureSessionConfigLocked(session, config)
	sectionIndex := s.nextIndex
	s.nextIndex++
	session.staged.sections[config][sectionName] = section{
		Type:      typ,
		Anonymous: false,
		Index:     sectionIndex,
		Values:    deepCopyMap(values),
	}
	session.staged.changes[config] = append(session.staged.changes[config], []any{"set", sectionName, typ})
	for key, value := range values {
		session.staged.changes[config] = append(session.staged.changes[config], []any{"set", sectionName, key, value})
	}
	s.writeRPCResult(w, id, []any{0, map[string]any{"section": sectionName}})
}

func (s *Server) handleUCISet(w http.ResponseWriter, id any, token string, args map[string]any) {
	config, _ := args["config"].(string)
	sectionName, _ := args["section"].(string)
	values, _ := args["values"].(map[string]any)
	if config == "" || sectionName == "" {
		s.writeRPCResult(w, id, []any{2})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[token]
	view := s.sessionViewLocked(token, config)
	entry, exists := view[sectionName]
	if !exists {
		s.writeRPCResult(w, id, []any{4})
		return
	}
	if values == nil {
		values = map[string]any{}
	}

	s.ensureSessionConfigLocked(session, config)
	stagedSection, hasStaged := session.staged.sections[config][sectionName]
	if !hasStaged {
		stagedSection = entry
		stagedSection.Values = deepCopyMap(entry.Values)
	}
	for key, value := range values {
		stagedSection.Values[key] = value
		session.staged.changes[config] = append(session.staged.changes[config], []any{"set", sectionName, key, value})
	}
	session.staged.sections[config][sectionName] = stagedSection
	s.writeRPCResult(w, id, []any{0})
}

func (s *Server) handleUCIDelete(w http.ResponseWriter, id any, token string, args map[string]any) {
	config, _ := args["config"].(string)
	sectionName, _ := args["section"].(string)
	if config == "" || sectionName == "" {
		s.writeRPCResult(w, id, []any{2})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if config == "dhcp" && sectionName == sentinelSectionName {
		s.writeRPCResult(w, id, []any{6})
		return
	}
	session := s.sessions[token]
	view := s.sessionViewLocked(token, config)
	if _, exists := view[sectionName]; !exists {
		s.writeRPCResult(w, id, []any{4})
		return
	}
	s.ensureSessionConfigLocked(session, config)
	session.staged.deleted[config][sectionName] = true
	delete(session.staged.sections[config], sectionName)
	session.staged.changes[config] = append(session.staged.changes[config], []any{"delete", sectionName})
	s.writeRPCResult(w, id, []any{0})
}

func (s *Server) handleUCIChanges(w http.ResponseWriter, id any, token string, args map[string]any) {
	config, _ := args["config"].(string)
	if config == "" {
		s.writeRPCResult(w, id, []any{2})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[token]
	s.ensureSessionConfigLocked(session, config)
	rows := make([]any, 0, len(session.staged.changes[config]))
	for _, row := range session.staged.changes[config] {
		rowCopy := make([]any, len(row))
		copy(rowCopy, row)
		rows = append(rows, rowCopy)
	}
	s.writeRPCResult(w, id, []any{0, map[string]any{"changes": rows}})
}

func (s *Server) handleUCIRevert(w http.ResponseWriter, id any, token string, args map[string]any) {
	config, _ := args["config"].(string)
	if config == "" {
		s.writeRPCResult(w, id, []any{2})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[token]
	delete(session.staged.sections, config)
	delete(session.staged.deleted, config)
	delete(session.staged.changes, config)
	s.writeRPCResult(w, id, []any{0})
}

func (s *Server) handleUCICommit(w http.ResponseWriter, id any, token string, args map[string]any) {
	config, _ := args["config"].(string)
	if config == "" {
		s.writeRPCResult(w, id, []any{2})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[token]
	s.commitConfigLocked(session, config)
	s.writeRPCResult(w, id, []any{0})
}

func (s *Server) handleUCIApply(w http.ResponseWriter, id any, token string, args map[string]any) {
	rollback, _ := args["rollback"].(bool)
	timeoutFloat, ok := args["timeout"].(float64)
	if !ok || timeoutFloat <= 0 {
		s.writeRPCResult(w, id, []any{2})
		return
	}
	timeout := time.Duration(int64(timeoutFloat)) * time.Second

	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[token]
	snapshot := deepCopyCommitted(s.committed)
	for config := range session.staged.sections {
		s.commitConfigLocked(session, config)
	}
	for config := range session.staged.deleted {
		s.commitConfigLocked(session, config)
	}
	for config := range session.staged.changes {
		s.commitConfigLocked(session, config)
	}
	if rollback {
		s.pendingApply = &pendingApply{
			sessionToken: token,
			dueAt:        s.now().Add(timeout),
			snapshot:     snapshot,
		}
	}
	s.writeRPCResult(w, id, []any{0})
}

func (s *Server) handleUCIConfirm(w http.ResponseWriter, id any, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingApply != nil {
		if s.pendingApply.sessionToken != token {
			s.writeRPCResult(w, id, []any{6})
			return
		}
		s.pendingApply = nil
	}
	s.writeRPCResult(w, id, []any{0})
}

func (s *Server) committedSectionLocked(config, sectionName string) (section, error) {
	pkg, ok := s.committed[config]
	if !ok {
		return section{}, fmt.Errorf("config %q not found", config)
	}
	entry, ok := pkg[sectionName]
	if !ok {
		return section{}, fmt.Errorf("section %q not found", sectionName)
	}
	return entry, nil
}

func (s *Server) setCommittedSectionLocked(config, sectionName string, entry section) {
	pkg, ok := s.committed[config]
	if !ok {
		pkg = map[string]section{}
		s.committed[config] = pkg
	}
	pkg[sectionName] = section{
		Type:      entry.Type,
		Anonymous: entry.Anonymous,
		Index:     entry.Index,
		Values:    deepCopyMap(entry.Values),
	}
}

func (s *Server) ensureSessionConfigLocked(session *mockSession, config string) {
	if session.staged.sections[config] == nil {
		session.staged.sections[config] = map[string]section{}
	}
	if session.staged.deleted[config] == nil {
		session.staged.deleted[config] = map[string]bool{}
	}
	if session.staged.changes[config] == nil {
		session.staged.changes[config] = [][]any{}
	}
}

func (s *Server) commitConfigLocked(session *mockSession, config string) {
	if deleted := session.staged.deleted[config]; deleted != nil {
		pkg, ok := s.committed[config]
		if ok {
			for sectionName, shouldDelete := range deleted {
				if shouldDelete {
					delete(pkg, sectionName)
				}
			}
		}
	}
	if stagedSections := session.staged.sections[config]; stagedSections != nil {
		pkg, ok := s.committed[config]
		if !ok {
			pkg = map[string]section{}
			s.committed[config] = pkg
		}
		for sectionName, entry := range stagedSections {
			pkg[sectionName] = section{
				Type:      entry.Type,
				Anonymous: entry.Anonymous,
				Index:     entry.Index,
				Values:    deepCopyMap(entry.Values),
			}
		}
	}
	delete(session.staged.sections, config)
	delete(session.staged.deleted, config)
	delete(session.staged.changes, config)
}

func (s *Server) sessionViewLocked(token, config string) map[string]section {
	view := map[string]section{}
	if pkg, ok := s.committed[config]; ok {
		for sectionName, entry := range pkg {
			view[sectionName] = section{
				Type:      entry.Type,
				Anonymous: entry.Anonymous,
				Index:     entry.Index,
				Values:    deepCopyMap(entry.Values),
			}
		}
	}

	session := s.sessions[token]
	if session == nil {
		return view
	}
	s.ensureSessionConfigLocked(session, config)

	for sectionName, shouldDelete := range session.staged.deleted[config] {
		if shouldDelete {
			delete(view, sectionName)
		}
	}
	for sectionName, entry := range session.staged.sections[config] {
		view[sectionName] = section{
			Type:      entry.Type,
			Anonymous: entry.Anonymous,
			Index:     entry.Index,
			Values:    deepCopyMap(entry.Values),
		}
	}
	return view
}

func (s *Server) popFailureLocked(object, method string) *OneShotFailure {
	key := failureKey(object, method)
	queue := s.failures[key]
	if len(queue) == 0 {
		return nil
	}
	failure := queue[0]
	if len(queue) == 1 {
		delete(s.failures, key)
	} else {
		s.failures[key] = queue[1:]
	}
	return &failure
}

func (s *Server) applyPendingRollbackLocked() {
	if s.pendingApply == nil {
		return
	}
	if s.now().Before(s.pendingApply.dueAt) {
		return
	}
	s.committed = deepCopyCommitted(s.pendingApply.snapshot)
	s.pendingApply = nil
}

func failureKey(object, method string) string {
	return object + "." + method
}

func isAllowedMethod(object, method string) bool {
	switch object + "." + method {
	case "session.login",
		"uci.get",
		"uci.add",
		"uci.set",
		"uci.delete",
		"uci.changes",
		"uci.revert",
		"uci.commit",
		"uci.apply",
		"uci.confirm":
		return true
	default:
		return false
	}
}

func sectionToValues(sectionName string, entry section) map[string]any {
	values := map[string]any{
		".name":      sectionName,
		".type":      entry.Type,
		".anonymous": entry.Anonymous,
		".index":     entry.Index,
	}
	for key, value := range entry.Values {
		values[key] = value
	}
	return values
}

func (s *Server) writeRPCResult(w http.ResponseWriter, id any, result []any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func (s *Server) writeRPCError(w http.ResponseWriter, id any, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func deepCopyCommitted(in map[string]map[string]section) map[string]map[string]section {
	out := map[string]map[string]section{}
	for config, sections := range in {
		cfgOut := map[string]section{}
		for name, sec := range sections {
			cfgOut[name] = section{
				Type:      sec.Type,
				Anonymous: sec.Anonymous,
				Index:     sec.Index,
				Values:    deepCopyMap(sec.Values),
			}
		}
		out[config] = cfgOut
	}
	return out
}

func deepCopyMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
