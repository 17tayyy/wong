package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/17tayyy/wong/internal/check"
	"github.com/17tayyy/wong/internal/remote"
)

const deploy = "---\nname: deploy-runbook\ndescription: How a release goes out\nkind: reference\nscope: project:acme\n---\nMerge to `main`, then promote.\n"

type client struct {
	t     *testing.T
	url   string
	token string
}

func setup(t *testing.T) (*remote.Store, client) {
	t.Helper()
	store, err := remote.Open(filepath.Join(t.TempDir(), "wong.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for path, content := range map[string]string{
		"projects/acme/deploy-runbook.md": deploy,
		"projects/acme/MEMORY.md":         "- [deploy-runbook](deploy-runbook.md) — How a release goes out\n",
		"me/MEMORY.md":                    "- [use-uv](use-uv.md) — uv, never pip\n",
		"me/use-uv.md":                    "---\nname: use-uv\ndescription: uv, never pip\nkind: preference\nscope: global\n---\nUse uv for Python.\n",
		"remotes.yaml":                    "acme/api:\n  project: acme\n  dir: api\n",
	} {
		if _, _, err := store.Apply("desktop", []remote.Upload{{Path: path, Sha: remote.Sha([]byte(content)), Content: []byte(content)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ReportFindings("desktop", "acme", []check.Finding{{Note: "projects/acme/deploy-runbook.md", Severity: check.Review, Ref: "api/deploy.sh", Message: "api/deploy.sh changed after the note"}}); err != nil {
		t.Fatal(err)
	}
	token, err := store.NewToken("codex")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(store, slog.New(slog.NewTextHandler(io.Discard, nil)), "test"))
	t.Cleanup(srv.Close)
	return store, client{t, srv.URL, token}
}

// post sends one JSON-RPC message with the headers given and returns the status and the decoded answer.
func (c client) post(body string, headers map[string]string) (int, map[string]any) {
	c.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, c.url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

const meta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`

// modern sends a request the way a 2026-07-28 client does, headers mirroring the body.
func (c client) modern(id int, method, name, args string) (int, map[string]any) {
	c.t.Helper()
	body := `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"` + method + `","params":{` + meta
	headers := map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": method}
	if name != "" {
		body += `,"name":"` + name + `","arguments":` + args
		headers["Mcp-Name"] = name
	}
	return c.post(body+"}}", headers)
}

// tool calls a tool as a legacy client and returns its text and whether it failed.
func (c client) tool(name, args string) (string, bool) {
	c.t.Helper()
	code, out := c.post(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`, map[string]string{"MCP-Protocol-Version": "2025-06-18"})
	result, ok := out["result"].(map[string]any)
	if code != http.StatusOK || !ok {
		c.t.Fatalf("%s: %d %v", name, code, out)
	}
	return result["content"].([]any)[0].(map[string]any)["text"].(string), result["isError"].(bool)
}

func TestLegacyClient(t *testing.T) {
	_, c := setup(t)
	code, out := c.post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"codex","version":"1"}}}`, nil)
	result := out["result"].(map[string]any)
	if code != http.StatusOK || result["protocolVersion"] != "2025-06-18" || !strings.Contains(result["instructions"].(string), "get_context") {
		t.Fatalf("initialize: %d %v", code, out)
	}
	if code, _ := c.post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, map[string]string{"MCP-Protocol-Version": "2025-06-18"}); code != http.StatusAccepted {
		t.Errorf("initialized notification: %d", code)
	}
	_, out = c.post(`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`, nil)
	if out["result"].(map[string]any)["protocolVersion"] != "2025-11-25" {
		t.Errorf("an unknown version was not answered with the latest legacy one: %v", out)
	}
	_, out = c.post(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`, map[string]string{"MCP-Protocol-Version": "2025-06-18"})
	if tools := out["result"].(map[string]any)["tools"].([]any); len(tools) != 8 {
		t.Errorf("tools/list = %d tools", len(tools))
	}
	if code, _ := c.post(`{"jsonrpc":"2.0","id":4,"method":"tools/list"}`, map[string]string{"MCP-Protocol-Version": "1999-01-01"}); code != http.StatusBadRequest {
		t.Errorf("an unsupported version header: %d", code)
	}
}

func TestModernClient(t *testing.T) {
	_, c := setup(t)
	code, out := c.modern(1, "server/discover", "", "")
	result := out["result"].(map[string]any)
	if code != http.StatusOK || result["resultType"] != "complete" || result["supportedVersions"].([]any)[0] != "2026-07-28" {
		t.Fatalf("server/discover: %d %v", code, out)
	}
	if code, out := c.modern(2, "tools/call", "get_context", `{"repo":"git@github.com:acme/api.git"}`); code != http.StatusOK || !strings.Contains(out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string), "deploy-runbook") {
		t.Errorf("tools/call: %d %v", code, out)
	}

	body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{` + meta + `,"name":"read_note","arguments":{}}}`
	if code, out := c.post(body, map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call", "Mcp-Name": "delete_note"}); code != http.StatusBadRequest || out["error"].(map[string]any)["code"].(float64) != codeHeaderMismatch {
		t.Errorf("a name header that does not match: %d %v", code, out)
	}
	if code, out := c.post(body, map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call", "Mcp-Name": headerSafe("read_note")}); code != http.StatusOK {
		t.Errorf("matching headers: %d %v", code, out)
	}
	old := strings.Replace(body, "2026-07-28", "2025-01-01", 1)
	if code, out := c.post(old, map[string]string{"MCP-Protocol-Version": "2025-01-01", "Mcp-Method": "tools/call", "Mcp-Name": "read_note"}); code != http.StatusBadRequest || out["error"].(map[string]any)["code"].(float64) != codeUnsupported {
		t.Errorf("an unsupported version: %d %v", code, out)
	}
	if code, out := c.post(`{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`, map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/list"}); code != http.StatusBadRequest || out["error"].(map[string]any)["code"].(float64) != codeInvalidParams {
		t.Errorf("a request without client capabilities: %d %v", code, out)
	}
	if code, out := c.modern(5, "prompts/list", "", ""); code != http.StatusNotFound || out["error"].(map[string]any)["code"].(float64) != codeNoMethod {
		t.Errorf("an unknown method: %d %v", code, out)
	}
}

func TestTheEndpointGuardsItself(t *testing.T) {
	_, c := setup(t)
	resp, _ := http.Get(c.url)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", resp.StatusCode)
	}
	if code, _ := (client{t, c.url, "wong_wrong"}).post(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, nil); code != http.StatusUnauthorized {
		t.Errorf("an unknown token: %d", code)
	}
	if code, _ := c.post(`{"jsonrpc":"2.0","id":1,"method":"ping"}`, map[string]string{"Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Errorf("a cross-origin request: %d", code)
	}
	if code, _ := c.post(`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, nil); code != http.StatusBadRequest {
		t.Errorf("a batch: %d", code)
	}
}

func TestTools(t *testing.T) {
	store, c := setup(t)

	text, failed := c.tool("get_context", `{"project":"acme"}`)
	for _, want := range []string{"uv, never pip", "deploy-runbook.md", "[review] projects/acme/deploy-runbook.md, api/deploy.sh"} {
		if failed || !strings.Contains(text, want) {
			t.Errorf("get_context lacks %q:\n%s", want, text)
		}
	}
	if text, _ := c.tool("get_context", `{}`); !strings.Contains(text, "- acme (1 notes)") {
		t.Errorf("get_context without a project:\n%s", text)
	}
	if text, failed := c.tool("get_context", `{"project":"nothing"}`); !failed || !strings.Contains(text, "the projects are: acme") {
		t.Errorf("get_context of a missing project: %s", text)
	}

	if text, _ := c.tool("search_notes", `{"query":"merge MAIN"}`); !strings.Contains(text, "projects/acme/deploy-runbook.md") || !strings.Contains(text, "> Merge to") {
		t.Errorf("search_notes:\n%s", text)
	}
	text, _ = c.tool("read_note", `{"note":"deploy-runbook"}`)
	if !strings.Contains(text, "sha: "+remote.Sha([]byte(deploy))) || !strings.Contains(text, "Open on this note:") {
		t.Errorf("read_note:\n%s", text)
	}

	if text, failed := c.tool("edit_note", `{"note":"deploy-runbook","old_text":"then promote","new_text":"then promote from the release workflow"}`); failed {
		t.Fatalf("edit_note: %s", text)
	}
	files, _ := store.Files()
	for _, f := range files {
		if f.Path == "projects/acme/deploy-runbook.md" && !strings.Contains(string(f.Content), "from the release workflow") {
			t.Errorf("after edit_note:\n%s", f.Content)
		}
	}
	if text, failed := c.tool("edit_note", `{"note":"deploy-runbook","old_text":"nowhere","new_text":"x"}`); !failed || !strings.Contains(text, "not in") {
		t.Errorf("edit_note with missing text: %s", text)
	}
	if text, failed := c.tool("edit_note", `{"note":"deploy-runbook","content":"---\nname: x\n---\n","sha":"stale"}`); !failed {
		t.Errorf("edit_note over a stale sha: %s", text)
	}

	text, failed = c.tool("write_note", `{"to":"acme","name":"Rate limits","description":"Per API key","kind":"decision","body":"Decided on 2026-10-08.","sources":["linear:ENG-40"]}`)
	if failed || !strings.Contains(text, "projects/acme/rate-limits.md") {
		t.Fatalf("write_note: %s", text)
	}
	if text, failed := c.tool("write_note", `{"to":"acme","name":"x","description":"y","kind":"opinion","body":"z"}`); !failed || !strings.Contains(text, "unknown kind") {
		t.Errorf("write_note with a bad kind: %s", text)
	}
	if text, failed := c.tool("verify_note", `{"note":"rate-limits","sources":["commit:api@abc1234"]}`); failed {
		t.Errorf("verify_note: %s", text)
	}
	if text, failed := c.tool("move_note", `{"note":"rate-limits","to":"me"}`); failed || !strings.Contains(text, "me/rate-limits.md") {
		t.Errorf("move_note: %s", text)
	}
	if text, failed := c.tool("delete_note", `{"note":"me/rate-limits.md"}`); failed || !strings.Contains(text, "undo") {
		t.Errorf("delete_note: %s", text)
	}
	log, _ := store.Log(1, 0)
	if log[0].Machine != "codex" || !strings.HasPrefix(log[0].Op, "delete:") {
		t.Errorf("the deletion is not signed by the token: %+v", log[0])
	}
	if text, failed := c.tool("nothing", `{}`); !failed || !strings.Contains(text, "get_context") {
		t.Errorf("an unknown tool: %s", text)
	}
}

func TestBridge(t *testing.T) {
	_, c := setup(t)
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_note","arguments":{"note":"use-uv"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{` + meta + `,"name":"read_note","arguments":{"note":"use-uv"}}}`,
		`not json`,
	}, "\n") + "\n"
	var out bytes.Buffer
	if err := Bridge(context.Background(), strings.NewReader(in), &out, c.url, c.token, http.DefaultClient); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("bridge wrote %d lines:\n%s", len(lines), out.String())
	}
	for i, want := range []string{`"protocolVersion":"2025-06-18"`, `Use uv for Python.`, `"resultType":"complete"`, `"code":-32700`} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d lacks %s: %s", i+1, want, lines[i])
		}
	}
}

func TestAgentsKeepTagsLinksAndNotesTidy(t *testing.T) {
	_, c := setup(t)
	write := func(args string) (string, bool) { return c.tool("write_note", args) }

	if text, failed := write(`{"to":"acme","name":"CI pipeline","description":"How CI builds images","kind":"reference","body":"Builds on every push. Tracked in ENG-15.","tags":["#Deploy","CI"]}`); failed {
		t.Fatalf("write_note with tags: %s", text)
	}
	text, failed := write(`{"to":"acme","name":"Preview apps","description":"One preview per pull request","kind":"reference","body":"Built by CI.","tags":["deployment"]}`)
	if !failed || !strings.Contains(text, `use "deploy"`) || !strings.Contains(text, "Tags in use: ci (1), deploy (1)") {
		t.Errorf("a spelling of a tag in use: %s", text)
	}
	text, failed = write(`{"to":"acme","name":"Preview apps","description":"One preview per pull request","kind":"reference","body":"Built as in [[ci-pipelin]].","tags":["deploy"]}`)
	if !failed || !strings.Contains(text, "did you mean [[ci-pipeline]]") {
		t.Errorf("a link to no note: %s", text)
	}
	text, failed = write(`{"to":"acme","name":"Release process","description":"How a release goes out","kind":"reference","body":"Merge, then promote."}`)
	if !failed || !strings.Contains(text, "projects/acme/deploy-runbook.md") {
		t.Errorf("a note on a subject already covered: %s", text)
	}
	if text, failed = write(`{"to":"acme","name":"Release process","description":"How a release goes out","kind":"reference","body":"Merge, then promote.","distinct":true}`); failed {
		t.Errorf("distinct: %s", text)
	}
	text, failed = write(`{"to":"acme","name":"Image tags","description":"Images are tagged with the commit","kind":"reference","body":"Tracked in ENG-15.","tags":["deploy"]}`)
	if failed || !strings.Contains(text, "[[CI pipeline]] (projects/acme/ci-pipeline.md, shares ENG-15, #deploy)") {
		t.Errorf("related notes after writing: %s", text)
	}

	if text, failed := c.tool("edit_note", `{"note":"deploy-runbook","old_text":"then promote","new_text":"then promote, see [[nowhere]]"}`); !failed || !strings.Contains(text, "[[nowhere]] reaches no note") {
		t.Errorf("edit_note adding a link to no note: %s", text)
	}
	if text, failed := c.tool("edit_note", `{"note":"ci-pipeline","old_text":"tags: [deploy, ci]","new_text":"tags: [deploy, ci, Security]"}`); !failed || !strings.Contains(text, `as "security"`) {
		t.Errorf("edit_note adding an unnormalized tag: %s", text)
	}
	text, _ = c.tool("read_note", `{"note":"ci-pipeline"}`)
	if !strings.Contains(text, "Related but not linked yet") || !strings.Contains(text, "[[Image tags]] (projects/acme/image-tags.md, shares ENG-15, #deploy)") {
		t.Errorf("read_note does not suggest links:\n%s", text)
	}
	if text, _ := c.tool("get_context", `{"project":"acme"}`); !strings.Contains(text, "Reuse these instead of new spellings: deploy (2), ci (1)") {
		t.Errorf("get_context does not list the tags:\n%s", text)
	}
}
