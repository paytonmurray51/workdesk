package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/paytonmurray51/workdesk/internal/db"
)

// bearer makes a request with an Authorization header and no cookie.
func (h *harness) bearer(tok, method, url, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

// todayID returns the id of a board's Today list (via the session).
func (h *harness) todayID(board string) string {
	h.t.Helper()
	for _, l := range decode[[]db.List](h.t, h.do("GET", "/api/boards/"+board+"/lists", "")) {
		if l.Name == "Today" {
			return itoa(l.ID)
		}
	}
	h.t.Fatalf("board %s has no Today list", board)
	return ""
}

func (h *harness) newToken(board, name string) string {
	h.t.Helper()
	rec := h.do("POST", "/api/boards/"+board+"/tokens", `{"name":"`+name+`"}`)
	if rec.Code != http.StatusCreated {
		h.t.Fatalf("create token = %d %s", rec.Code, rec.Body)
	}
	return decode[map[string]any](h.t, rec)["secret"].(string)
}

func TestBoardTokenScope(t *testing.T) {
	h := newHarness(t)
	h.login("admin@example.com")
	tok := h.newToken("1", "Morning brief")

	boards := decode[[]db.Board](t, h.bearer(tok, "GET", "/api/boards", ""))
	if len(boards) != 1 || boards[0].Name != "Work" {
		t.Fatalf("token boards = %+v", boards)
	}
	work, personal := h.todayID("1"), h.todayID("2")
	if rec := h.bearer(tok, "GET", "/api/boards/2/lists", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("other board = %d, want 404", rec.Code)
	}
	if rec := h.bearer(tok, "POST", "/api/lists/"+personal+"/items", `{"title":"x"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("other board's list = %d, want 404", rec.Code)
	}
	if rec := h.bearer(tok, "POST", "/api/lists/"+work+"/items", `{"title":"from token"}`); rec.Code != http.StatusCreated {
		t.Fatalf("own list = %d %s", rec.Code, rec.Body)
	}
	// Moving an item onto another board's list is refused.
	items := decode[[]db.Item](t, h.bearer(tok, "GET", "/api/lists/"+work+"/items", ""))
	if rec := h.bearer(tok, "PATCH", "/api/items/"+itoa(items[0].ID), `{"list_id":`+personal+`}`); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-board move = %d", rec.Code)
	}
	// Tokens can't manage tokens.
	if rec := h.bearer(tok, "GET", "/api/boards/1/tokens", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("token listing tokens = %d", rec.Code)
	}
	if rec := h.bearer("wdk_nope", "GET", "/api/boards", ""); rec.Code != http.StatusUnauthorized ||
		!strings.Contains(rec.Header().Get("WWW-Authenticate"), "invalid_token") {
		t.Fatalf("bad token = %d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}

	list := decode[[]db.APIToken](t, h.do("GET", "/api/boards/1/tokens", ""))
	if len(list) != 1 || list[0].LastUsedAt == nil {
		t.Fatalf("tokens = %+v", list)
	}
	if rec := h.do("DELETE", "/api/tokens/"+itoa(list[0].ID), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rec := h.bearer(tok, "GET", "/api/boards", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("deleted token = %d", rec.Code)
	}
}

func TestRESTPosts(t *testing.T) {
	h := newHarness(t)
	h.login("admin@example.com")
	tok := h.newToken("1", "routine")

	rec := h.bearer(tok, "POST", "/api/boards/1/briefings", `{"markdown":"# Hi","stats":{"emails":3},"source_id":"b1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("briefing = %d %s", rec.Code, rec.Body)
	}
	h.bearer(tok, "POST", "/api/boards/1/briefings", `{"markdown":"# Hi again","source_id":"b1"}`)
	latest := decode[db.Briefing](t, h.do("GET", "/api/boards/1/briefings/latest", ""))
	if latest.Markdown != "# Hi again" {
		t.Fatalf("latest = %+v", latest)
	}

	if rec := h.bearer(tok, "POST", "/api/boards/1/inbox", `[{"title":"Invoice","source_id":"m1"},{"title":"PR review"}]`); rec.Code != http.StatusCreated {
		t.Fatalf("inbox = %d %s", rec.Code, rec.Body)
	}
	if n := len(decode[[]db.InboxItem](t, h.do("GET", "/api/boards/1/inbox?status=new", ""))); n != 2 {
		t.Fatalf("inbox new = %d", n)
	}

	if rec := h.bearer(tok, "PUT", "/api/boards/1/widgets/calendar", `{"events":[{"title":"Standup","start":"07:00"}]}`); rec.Code != 200 {
		t.Fatalf("widget = %d %s", rec.Code, rec.Body)
	}
	if rec := h.bearer(tok, "PUT", "/api/boards/1/widgets/Bad%20Name", `{}`); rec.Code != 400 {
		t.Fatalf("bad widget name = %d", rec.Code)
	}

	rec = h.bearer(tok, "PUT", "/api/boards/1/days/2026-10-12/timeboxes",
		`{"boxes":[{"start":"07:00","end":"07:15","kind":"meeting","title":"Standup"}]}`)
	if rec.Code != 200 || len(decode[[]db.Timebox](t, rec)) != 1 {
		t.Fatalf("timeboxes = %d %s", rec.Code, rec.Body)
	}
	if rec := h.bearer(tok, "PUT", "/api/boards/1/days/2026-10-12/timeboxes", `[{"start":"9","end":"10","kind":"task","title":"x"}]`); rec.Code != 400 {
		t.Fatalf("bad timebox = %d", rec.Code)
	}

	rec = h.bearer(tok, "POST", "/api/boards/1/lists", `{"name":"Backlog"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create list = %d", rec.Code)
	}
	if rec := h.bearer(tok, "POST", "/api/boards/1/lists", `{"name":"Backlog"}`); rec.Code != http.StatusOK {
		t.Fatalf("repeat create list = %d", rec.Code)
	}
}

func pkcePair() (verifier, challenge string) {
	verifier = strings.Repeat("v", 50)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// oauthConnect runs register → authorize → consent (boards) → token like a connector would.
func (h *harness) oauthConnect(boards ...string) (clientID string, tokens map[string]any) {
	t := h.t
	t.Helper()
	cb := "https://claude.ai/api/mcp/auth_callback"
	rec := h.do("POST", "/oauth/register", `{"client_name":"Claude","redirect_uris":["`+cb+`"],"token_endpoint_auth_method":"none"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d %s", rec.Code, rec.Body)
	}
	reg := decode[map[string]any](t, rec)
	clientID = reg["client_id"].(string)
	if _, ok := reg["client_secret"]; ok {
		t.Fatal("public client got a secret")
	}

	verifier, challenge := pkcePair()
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {cb}, "state": {"st8"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"}, "resource": {"https://workdesk.example.com/mcp"}}
	rec = h.do("GET", "/oauth/authorize?"+q.Encode(), "")
	if rec.Code != 200 {
		t.Fatalf("authorize = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' https://claude.ai") {
		t.Fatalf("consent CSP = %q", csp)
	}
	m := regexp.MustCompile(`name="req" value="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("no req in consent page: %s", rec.Body)
	}
	form := url.Values{"req": {m[1]}, "decision": {"allow"}, "board": boards}
	req := httptest.NewRequest("POST", "/oauth/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(h.cookie)
	rec = httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || loc.Query().Get("state") != "st8" || loc.Query().Get("code") == "" {
		t.Fatalf("consent = %d %s", rec.Code, loc)
	}

	tokForm := url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")}, "client_id": {clientID},
		"redirect_uri": {cb}, "code_verifier": {verifier}}
	rec = h.form("/oauth/token", tokForm)
	if rec.Code != 200 {
		t.Fatalf("token = %d %s", rec.Code, rec.Body)
	}
	tokens = decode[map[string]any](t, rec)
	// The code is single-use.
	if rec := h.form("/oauth/token", tokForm); rec.Code != 400 {
		t.Fatalf("code reuse = %d", rec.Code)
	}
	return clientID, tokens
}

func (h *harness) form(path string, v url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

func TestOAuthMetadata(t *testing.T) {
	h := newHarness(t)
	prm := decode[map[string]any](t, h.do("GET", "/.well-known/oauth-protected-resource/mcp", ""))
	if prm["resource"] != "https://workdesk.example.com/mcp" {
		t.Fatalf("PRM = %v", prm)
	}
	as := decode[map[string]any](t, h.do("GET", "/.well-known/oauth-authorization-server", ""))
	if as["issuer"] != "https://workdesk.example.com" || as["registration_endpoint"] != "https://workdesk.example.com/oauth/register" {
		t.Fatalf("AS meta = %v", as)
	}
	rec := h.do("POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	if rec.Code != 401 || !strings.Contains(rec.Header().Get("WWW-Authenticate"),
		`resource_metadata="https://workdesk.example.com/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("unauthenticated MCP = %d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	for _, bad := range []string{`{"redirect_uris":["http://evil.example/cb"]}`, `{"redirect_uris":[]}`, `{"redirect_uris":["https://x/cb#frag"]}`} {
		if rec := h.do("POST", "/oauth/register", bad); rec.Code != 400 {
			t.Errorf("register %s = %d", bad, rec.Code)
		}
	}
}

func TestOAuthFlowScopesBoards(t *testing.T) {
	h := newHarness(t)
	// Not signed in: authorize sends the browser to Google sign-in and back.
	clientRec := h.do("POST", "/oauth/register", `{"redirect_uris":["https://claude.ai/cb"],"token_endpoint_auth_method":"none"}`)
	cid := decode[map[string]any](t, clientRec)["client_id"].(string)
	_, ch := pkcePair()
	rec := h.do("GET", "/oauth/authorize?response_type=code&client_id="+cid+"&code_challenge="+ch+"&code_challenge_method=S256&state=x", "")
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/login?next=%2Foauth%2Fauthorize") {
		t.Fatalf("signed-out authorize = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// Without PKCE the request is bounced back to the client with an error.
	rec = h.do("GET", "/oauth/authorize?response_type=code&client_id="+cid+"&state=x", "")
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "error=invalid_request") {
		t.Fatalf("no PKCE = %q", loc)
	}

	h.login("admin@example.com")
	clientID, tokens := h.oauthConnect("2")
	access := tokens["access_token"].(string)

	boards := decode[[]db.Board](t, h.bearer(access, "GET", "/api/boards", ""))
	if len(boards) != 1 || boards[0].Name != "Personal" {
		t.Fatalf("grant boards = %+v", boards)
	}
	if rec := h.bearer(access, "GET", "/api/boards/1/lists", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("ungranted board = %d", rec.Code)
	}

	rec = h.form("/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tokens["refresh_token"].(string)}, "client_id": {clientID}})
	if rec.Code != 200 {
		t.Fatalf("refresh = %d %s", rec.Code, rec.Body)
	}
	newAccess := decode[map[string]any](t, rec)["access_token"].(string)
	if rec := h.bearer(newAccess, "GET", "/api/boards", ""); rec.Code != 200 {
		t.Fatalf("refreshed access = %d", rec.Code)
	}

	conns := decode[[]db.Grant](t, h.do("GET", "/api/connections", ""))
	if len(conns) != 1 || conns[0].ClientName != "Claude" {
		t.Fatalf("connections = %+v", conns)
	}
	if rec := h.do("DELETE", "/api/connections/"+itoa(conns[0].ID), ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d", rec.Code)
	}
	if rec := h.bearer(newAccess, "GET", "/api/boards", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked access = %d", rec.Code)
	}
}

func TestOAuthWrongVerifier(t *testing.T) {
	h := newHarness(t)
	h.login("admin@example.com")
	cb := "https://claude.ai/cb"
	cid := decode[map[string]any](t, h.do("POST", "/oauth/register", `{"redirect_uris":["`+cb+`"],"token_endpoint_auth_method":"none"}`))["client_id"].(string)
	_, ch := pkcePair()
	rec := h.do("GET", "/oauth/authorize?response_type=code&client_id="+cid+"&redirect_uri="+url.QueryEscape(cb)+"&code_challenge="+ch+"&code_challenge_method=S256", "")
	m := regexp.MustCompile(`name="req" value="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	req := httptest.NewRequest("POST", "/oauth/authorize", strings.NewReader(url.Values{"req": {m[1]}, "decision": {"allow"}, "board": {"1"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(h.cookie)
	rec = httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site consent = %d", rec.Code)
	}
	req.Header.Del("Sec-Fetch-Site")
	req.Body = http.NoBody
	req = httptest.NewRequest("POST", "/oauth/authorize", strings.NewReader(url.Values{"req": {m[1]}, "decision": {"allow"}, "board": {"1"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(h.cookie)
	rec = httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	tok := h.form("/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {loc.Query().Get("code")},
		"client_id": {cid}, "code_verifier": {strings.Repeat("w", 50)}})
	if tok.Code != 400 || !strings.Contains(tok.Body.String(), "invalid_grant") {
		t.Fatalf("wrong verifier = %d %s", tok.Code, tok.Body)
	}
}

type authTransport struct{ tok string }

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+a.tok)
	return http.DefaultTransport.RoundTrip(r)
}

func mcpSession(t *testing.T, h *harness, tok string) *mcp.ClientSession {
	t.Helper()
	ts := httptest.NewServer(h.h)
	t.Cleanup(ts.Close)
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: ts.URL + "/mcp", HTTPClient: &http.Client{Transport: authTransport{tok}}, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args any) (map[string]any, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		text := ""
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				text += tc.Text
			}
		}
		return map[string]any{"error": text}, false
	}
	b, _ := json.Marshal(res.StructuredContent)
	var out map[string]any
	json.Unmarshal(b, &out)
	return out, true
}

func TestMCPTools(t *testing.T) {
	h := newHarness(t)
	h.login("admin@example.com")
	_, tokens := h.oauthConnect("1", "2")
	cs := mcpSession(t, h, tokens["access_token"].(string))

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	want := "list_boards list_lists create_list list_open_items add_items update_item post_briefing push_inbox set_widget propose_timeboxes"
	for _, n := range strings.Fields(want) {
		if !strings.Contains(" "+strings.Join(names, " ")+" ", " "+n+" ") {
			t.Errorf("missing tool %s (have %v)", n, names)
		}
	}

	out, _ := callTool(t, cs, "list_boards", map[string]any{})
	if bs := out["boards"].([]any); len(bs) != 2 || bs[0].(map[string]any)["today"] == "" {
		t.Fatalf("list_boards = %v", out)
	}

	items := []map[string]any{{"title": "Review WD-7", "source_type": "jira", "source_id": "WD-7", "source_url": "https://jira/WD-7"}}
	if out, ok := callTool(t, cs, "add_items", map[string]any{"board": "work", "items": items}); !ok {
		t.Fatalf("add_items = %v", out)
	}
	items[0]["title"] = "Review WD-7 (updated)"
	callTool(t, cs, "add_items", map[string]any{"board": "Work", "items": items})
	out, _ = callTool(t, cs, "list_open_items", map[string]any{"board": "Work"})
	open := out["items"].([]any)
	if len(open) != 1 || open[0].(map[string]any)["title"] != "Review WD-7 (updated)" {
		t.Fatalf("idempotent add_items: %v", out)
	}
	id := open[0].(map[string]any)["id"]

	if out, ok := callTool(t, cs, "update_item", map[string]any{"id": id, "done": true}); !ok || out["done"] != true {
		t.Fatalf("update_item = %v", out)
	}
	out, _ = callTool(t, cs, "list_open_items", map[string]any{"board": "1", "list": "today"})
	if len(out["items"].([]any)) != 0 {
		t.Fatalf("after check: %v", out)
	}

	if out, ok := callTool(t, cs, "post_briefing", map[string]any{"board": "Work", "markdown": "# Monday", "stats": map[string]any{"emails": 4}, "source_id": "brief-2026-10-12"}); !ok {
		t.Fatalf("post_briefing = %v", out)
	}
	if out, ok := callTool(t, cs, "push_inbox", map[string]any{"board": "Work", "items": []map[string]any{{"title": "Invoice", "source_id": "m-1"}}}); !ok {
		t.Fatalf("push_inbox = %v", out)
	}
	if out, ok := callTool(t, cs, "set_widget", map[string]any{"board": "Work", "name": "calendar", "payload": map[string]any{"events": []any{}}}); !ok {
		t.Fatalf("set_widget = %v", out)
	}
	if out, ok := callTool(t, cs, "propose_timeboxes", map[string]any{"board": "Work", "date": "2026-10-12",
		"boxes": []map[string]any{{"start": "07:00", "end": "07:15", "kind": "meeting", "title": "Standup"}}}); !ok {
		t.Fatalf("propose_timeboxes = %v", out)
	}
	if out, ok := callTool(t, cs, "create_list", map[string]any{"board": "Personal", "name": "Errands"}); !ok || out["created"] != true {
		t.Fatalf("create_list = %v", out)
	}

	// Errors come back as tool errors with a useful message.
	if out, ok := callTool(t, cs, "list_lists", map[string]any{"board": "Nope"}); ok || !strings.Contains(out["error"].(string), "available: Work, Personal") {
		t.Fatalf("unknown board = %v", out)
	}
	if out, ok := callTool(t, cs, "propose_timeboxes", map[string]any{"board": "Work", "date": "2026-10-12",
		"boxes": []map[string]any{{"start": "10:00", "end": "09:00", "kind": "task", "title": "x"}}}); ok {
		t.Fatalf("bad timebox accepted: %v", out)
	}
}

func TestMCPBoardTokenIsScoped(t *testing.T) {
	h := newHarness(t)
	h.login("admin@example.com")
	cs := mcpSession(t, h, h.newToken("2", "personal routine"))

	out, _ := callTool(t, cs, "list_boards", map[string]any{})
	if bs := out["boards"].([]any); len(bs) != 1 || bs[0].(map[string]any)["name"] != "Personal" {
		t.Fatalf("token boards = %v", out)
	}
	if _, ok := callTool(t, cs, "add_items", map[string]any{"board": "Work", "items": []map[string]any{{"title": "x"}}}); ok {
		t.Fatal("token wrote to another board")
	}
	// With a single board, "board" may be omitted.
	if out, ok := callTool(t, cs, "add_items", map[string]any{"items": []map[string]any{{"title": "Buy milk"}}}); !ok {
		t.Fatalf("default board add = %v", out)
	}
}

func TestInboxTriageAPI(t *testing.T) {
	h := newHarness(t)
	h.login("admin@example.com")
	in := decode[[]db.InboxItem](t, h.do("POST", "/api/boards/1/inbox", `[{"type":"email","title":"Reply to Sam","source_id":"m-1"},{"title":"Later"},{"title":"Noise"}]`))

	rec := h.do("POST", "/api/inbox/"+itoa(in[0].ID)+"/add", "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("add = %d %s", rec.Code, rec.Body)
	}
	today := decode[[]db.Item](t, h.do("GET", "/api/lists/"+h.todayID("1")+"/items", ""))
	if len(today) != 1 || today[0].Title != "Reply to Sam" {
		t.Fatalf("today = %+v", today)
	}
	// Can't add an inbox item to another board's list.
	if rec := h.do("POST", "/api/inbox/"+itoa(in[1].ID)+"/add", `{"list_id":`+h.todayID("2")+`}`); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-board add = %d", rec.Code)
	}

	rec = h.do("PATCH", "/api/inbox/"+itoa(in[1].ID), `{"status":"snoozed"}`)
	if rec.Code != 200 || decode[db.InboxItem](t, rec).SnoozedUntil == nil {
		t.Fatalf("snooze = %d %s", rec.Code, rec.Body)
	}
	h.do("PATCH", "/api/inbox/"+itoa(in[2].ID), `{"status":"dismissed"}`)
	if rec := h.do("PATCH", "/api/inbox/"+itoa(in[2].ID), `{"status":"bogus"}`); rec.Code != 400 {
		t.Fatalf("bad status = %d", rec.Code)
	}
	if n := decode[[]db.InboxItem](t, h.do("GET", "/api/boards/1/inbox?status=new", "")); len(n) != 0 {
		t.Fatalf("new after triage = %+v", n)
	}

	// A board-2 token can't touch board-1 inbox items.
	tok := h.newToken("2", "personal")
	if rec := h.bearer(tok, "PATCH", "/api/inbox/"+itoa(in[1].ID), `{"status":"new"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("other board's inbox = %d", rec.Code)
	}
}

func TestClearBoard(t *testing.T) {
	h := newHarness(t)
	h.login("admin@example.com")
	work := h.todayID("1")
	h.do("POST", "/api/lists/"+work+"/items", `[{"title":"a"},{"title":"b"}]`)
	h.do("POST", "/api/boards/1/briefings", `{"markdown":"# x"}`)
	h.do("POST", "/api/lists/"+h.todayID("2")+"/items", `{"title":"personal stays"}`)

	_, tokens := h.oauthConnect("1", "2")
	cs := mcpSession(t, h, tokens["access_token"].(string))

	tools, _ := cs.ListTools(context.Background(), nil)
	for _, tl := range tools.Tools {
		if tl.Name == "clear_board" && (tl.Annotations == nil || tl.Annotations.DestructiveHint == nil || !*tl.Annotations.DestructiveHint) {
			t.Fatal("clear_board must be annotated destructive")
		}
	}

	for _, confirm := range []string{"", "work", "Personal", "yes"} {
		if out, ok := callTool(t, cs, "clear_board", map[string]any{"board": "Work", "confirm": confirm}); ok ||
			!strings.Contains(out["error"].(string), `"Work"`) {
			t.Fatalf("confirm %q = %v", confirm, out)
		}
	}
	if n := len(decode[[]db.Item](t, h.do("GET", "/api/lists/"+work+"/items", ""))); n != 2 {
		t.Fatalf("refused clear still erased: %d items", n)
	}

	out, ok := callTool(t, cs, "clear_board", map[string]any{"board": "Work", "confirm": "Work"})
	if !ok || out["items"] != float64(2) || out["briefings"] != float64(1) {
		t.Fatalf("clear = %v", out)
	}
	if n := len(decode[[]db.Item](t, h.do("GET", "/api/lists/"+work+"/items", ""))); n != 0 {
		t.Fatalf("after clear: %d items", n)
	}
	if n := len(decode[[]db.Item](t, h.do("GET", "/api/lists/"+h.todayID("2")+"/items", ""))); n != 1 {
		t.Fatal("other board was cleared")
	}
	if rec := h.do("GET", "/api/boards/1/lists", ""); len(decode[[]db.List](t, rec)) != 1 {
		t.Fatal("lists must survive a clear")
	}

	// REST: a Personal-only token can't clear Work, and needs the exact name.
	tok := h.newToken("2", "personal")
	if rec := h.bearer(tok, "POST", "/api/boards/1/clear", `{"confirm":"Work"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("token clearing other board = %d", rec.Code)
	}
	if rec := h.bearer(tok, "POST", "/api/boards/2/clear", `{"confirm":"personal"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong-case confirm = %d", rec.Code)
	}
	if rec := h.bearer(tok, "POST", "/api/boards/2/clear", `{"confirm":"Personal"}`); rec.Code != 200 {
		t.Fatalf("REST clear = %d %s", rec.Code, rec.Body)
	}
}
