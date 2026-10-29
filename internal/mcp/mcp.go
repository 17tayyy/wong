// Package mcp serves the brain to agents without files (Codex, Cursor, cloud workers) over the Model Context Protocol.
package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/17tayyy/wong/internal/remote"
)

// Modern versions carry their version on every request; legacy ones open with an initialize handshake.
var (
	modernVersions = []string{"2026-07-28"}
	legacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}
)

const (
	metaVersion      = "io.modelcontextprotocol/protocolVersion"
	metaCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo   = "io.modelcontextprotocol/serverInfo"

	codeParse          = -32700
	codeInvalidRequest = -32600
	codeNoMethod       = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	codeHeaderMismatch = -32020
	codeUnsupported    = -32022

	maxMessage = 4 << 20
)

const instructions = `wong is the user's external brain for coding agents: memory notes, preferences and skills, kept on their own server and checked against their sources.

At the start of a task, call get_context with the repo you work in. Read the notes that apply with read_note and follow the user's preferences in them. Keep the brain true: when a note is wrong or out of date, fix it with edit_note and mark it with verify_note; write what you learn with write_note. Point to the source (issue, commit, file) instead of copying its state. Ask the user before deleting a note, archiving one or moving one to another project. Archived notes stay out of searches unless asked for: look there only when the user asks about older work. Every change is signed with this connection's token and can be undone from the wong dashboard.`

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type params struct {
	Name      string                     `json:"name"`
	Arguments json.RawMessage            `json:"arguments"`
	Version   string                     `json:"protocolVersion"`
	Meta      map[string]json.RawMessage `json:"_meta"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Server answers MCP requests for the machines that hold a token, each on the brain its token opens, as store.
type Server struct {
	brains  remote.Brains
	store   *remote.Store
	log     *slog.Logger
	version string
	limits  *limiter
}

// Handler serves the MCP endpoint over Streamable HTTP to modern and legacy clients alike.
func Handler(brains remote.Brains, log *slog.Logger, version string) http.Handler {
	return &Server{brains: brains, log: log, version: version, limits: &limiter{buckets: map[string]*bucket{}}}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "the MCP endpoint takes POST", http.StatusMethodNotAllowed)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
			reply(w, http.StatusForbidden, nil, nil, &rpcError{Code: codeInvalidRequest, Message: "cross-origin request refused"})
			return
		}
	}
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	session, err := s.brains.Auth(token)
	if err != nil {
		s.log.Error("mcp", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if token == "" || session.Store == nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="wong", resource_metadata="`+remote.BaseURL(r)+`/.well-known/oauth-protected-resource/mcp"`)
		http.Error(w, "send a wong token: Authorization: Bearer <token>", http.StatusUnauthorized)
		return
	}
	if !s.limits.allow(session.Who()) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMessage))
	if err != nil {
		reply(w, http.StatusRequestEntityTooLarge, nil, nil, &rpcError{Code: codeInvalidRequest, Message: "message too large"})
		return
	}
	if b := bytes.TrimSpace(body); len(b) > 0 && b[0] == '[' {
		reply(w, http.StatusBadRequest, nil, nil, &rpcError{Code: codeInvalidRequest, Message: "batches are not supported: send one message per request"})
		return
	}
	var msg message
	if err := json.Unmarshal(body, &msg); err != nil || msg.Method == "" {
		reply(w, http.StatusBadRequest, nil, nil, &rpcError{Code: codeParse, Message: "not a JSON-RPC request or notification"})
		return
	}
	var p params
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			reply(w, http.StatusBadRequest, msg.ID, nil, &rpcError{Code: codeInvalidParams, Message: "params must be an object"})
			return
		}
	}
	brain := &Server{brains: s.brains, store: session.Store, log: s.log, version: s.version, limits: s.limits}
	if _, modern := p.Meta[metaVersion]; modern {
		brain.modern(w, r, session.Machine, msg, p)
		return
	}
	brain.legacy(w, r, session.Machine, msg, p)
}

// modern serves a request that carries its protocol version, after checking the headers mirror its body.
func (s *Server) modern(w http.ResponseWriter, r *http.Request, machine string, msg message, p params) {
	var version string
	_ = json.Unmarshal(p.Meta[metaVersion], &version)
	if r.Header.Get("MCP-Protocol-Version") != version {
		reply(w, http.StatusBadRequest, msg.ID, nil, &rpcError{Code: codeHeaderMismatch, Message: "Header mismatch: MCP-Protocol-Version does not match the request's protocol version"})
		return
	}
	if !slices.Contains(modernVersions, version) {
		reply(w, http.StatusBadRequest, msg.ID, nil, &rpcError{Code: codeUnsupported, Message: "Unsupported protocol version",
			Data: map[string]any{"supported": slices.Concat(modernVersions, legacyVersions), "requested": version}})
		return
	}
	if r.Header.Get("Mcp-Method") != msg.Method {
		reply(w, http.StatusBadRequest, msg.ID, nil, &rpcError{Code: codeHeaderMismatch, Message: "Header mismatch: Mcp-Method does not match the request's method"})
		return
	}
	if msg.Method == "tools/call" && headerValue(r.Header.Get("Mcp-Name")) != p.Name {
		reply(w, http.StatusBadRequest, msg.ID, nil, &rpcError{Code: codeHeaderMismatch, Message: "Header mismatch: Mcp-Name does not match the tool's name"})
		return
	}
	if _, ok := p.Meta[metaCapabilities]; !ok {
		reply(w, http.StatusBadRequest, msg.ID, nil, &rpcError{Code: codeInvalidParams, Message: "_meta lacks " + metaCapabilities})
		return
	}
	if msg.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	info := map[string]any{metaServerInfo: s.info()}
	var result map[string]any
	switch msg.Method {
	case "server/discover":
		result = map[string]any{"supportedVersions": slices.Concat(modernVersions, legacyVersions),
			"capabilities": map[string]any{"tools": map[string]any{}}, "instructions": instructions}
	case "tools/list":
		result = map[string]any{"tools": tools}
	case "tools/call":
		result = s.call(machine, p)
	default:
		reply(w, http.StatusNotFound, msg.ID, nil, &rpcError{Code: codeNoMethod, Message: "Method not found: " + msg.Method})
		return
	}
	result["resultType"] = "complete"
	result["_meta"] = info
	reply(w, http.StatusOK, msg.ID, result, nil)
}

// legacy serves a client of a handshake-based version, without sessions: every request stands alone.
func (s *Server) legacy(w http.ResponseWriter, r *http.Request, machine string, msg message, p params) {
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(legacyVersions, v) {
		reply(w, http.StatusBadRequest, msg.ID, nil, &rpcError{Code: codeInvalidRequest, Message: "Unsupported protocol version " + v,
			Data: map[string]any{"supported": slices.Concat(modernVersions, legacyVersions)}})
		return
	}
	if msg.ID == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	switch msg.Method {
	case "initialize":
		version := legacyVersions[0]
		if slices.Contains(legacyVersions, p.Version) {
			version = p.Version
		}
		reply(w, http.StatusOK, msg.ID, map[string]any{"protocolVersion": version,
			"capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": s.info(), "instructions": instructions}, nil)
	case "ping":
		reply(w, http.StatusOK, msg.ID, map[string]any{}, nil)
	case "tools/list":
		reply(w, http.StatusOK, msg.ID, map[string]any{"tools": tools}, nil)
	case "tools/call":
		reply(w, http.StatusOK, msg.ID, s.call(machine, p), nil)
	default:
		reply(w, http.StatusOK, msg.ID, nil, &rpcError{Code: codeNoMethod, Message: "Method not found: " + msg.Method})
	}
}

func (s *Server) info() map[string]string {
	return map[string]string{"name": "wong", "title": "wong", "version": s.version}
}

// call runs a tool and turns what it returns, or why it refused, into a tool result.
func (s *Server) call(machine string, p params) map[string]any {
	t, ok := byName[p.Name]
	if !ok {
		return failed("Unknown tool " + p.Name + ". The tools are " + strings.Join(toolNames(), ", ") + ".")
	}
	args := p.Arguments
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	text, err := t.run(s, machine, args)
	var ne *remote.NoteError
	switch {
	case errors.As(err, &ne):
		s.log.Info("mcp", "tool", p.Name, "machine", machine, "refused", ne.Msg)
		return failed(ne.Msg)
	case errors.Is(err, errArgs):
		s.log.Info("mcp", "tool", p.Name, "machine", machine, "refused", err.Error())
		return failed(err.Error())
	case err != nil:
		s.log.Error("mcp", "tool", p.Name, "machine", machine, "err", err)
		return failed("The wong server failed to run " + p.Name + "; nothing was changed. Its log has the details.")
	}
	s.log.Info("mcp", "tool", p.Name, "machine", machine)
	return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": false}
}

func failed(text string) map[string]any {
	return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": true}
}

func reply(w http.ResponseWriter, status int, id json.RawMessage, result any, rpcErr *rpcError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response{JSONRPC: "2.0", ID: id, Result: result, Error: rpcErr})
}

const sentinel = "=?base64?"

// headerValue decodes a header value sent in the Base64 sentinel form, or returns it as it is.
func headerValue(v string) string {
	if inner, ok := strings.CutPrefix(v, sentinel); ok && strings.HasSuffix(inner, "?=") {
		if data, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(inner, "?=")); err == nil {
			return string(data)
		}
	}
	return v
}

// headerSafe encodes a value for a header when it is not plain printable ASCII.
func headerSafe(v string) string {
	plain := v == strings.TrimSpace(v) && !(strings.HasPrefix(v, sentinel) && strings.HasSuffix(v, "?="))
	for _, c := range v {
		if c < 0x20 || c > 0x7e {
			plain = false
		}
	}
	if plain {
		return v
	}
	return sentinel + base64.StdEncoding.EncodeToString([]byte(v)) + "?="
}

// limiter keeps each machine to a steady rate of requests, with room for a burst.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

const (
	ratePerSecond = 10
	burst         = 40
)

func (l *limiter) allow(machine string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[machine]
	if !ok {
		b = &bucket{tokens: burst, last: now}
		l.buckets[machine] = b
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*ratePerSecond)
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
