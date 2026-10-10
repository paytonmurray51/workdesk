package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/paytonmurray51/workdesk/internal/auth"
	"github.com/paytonmurray51/workdesk/internal/config"
	"github.com/paytonmurray51/workdesk/internal/db"
)

type harness struct {
	t      *testing.T
	srv    *Server
	h      http.Handler
	cookie *http.Cookie
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cfg := &config.Config{
		BaseURL:       "https://workdesk.example.com",
		SessionSecret: []byte("0123456789abcdef0123456789abcdef"),
		AdminEmails:   config.ParseEmails("admin@example.com"),
	}
	ui := fstest.MapFS{
		"index.html":    {Data: []byte("<!doctype html><title>Workdesk</title>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	s := New(cfg, store, ui)
	return &harness{t: t, srv: s, h: s.Handler()}
}

// login runs the post-Google half of sign-in and keeps the session cookie.
func (h *harness) login(email string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.srv.onLogin(rec, httptest.NewRequest("GET", "/auth/callback", nil),
		auth.Identity{Sub: "sub-" + email, Email: email, Name: "Test"})
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookie {
			h.cookie = c
		}
	}
	return rec
}

func (h *harness) do(method, url, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.cookie != nil {
		req.AddCookie(h.cookie)
	}
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestHealthz(t *testing.T) {
	h := newHarness(t)
	rec := h.do("GET", "/healthz", "")
	if rec.Code != 200 || rec.Body.String() != "ok\n" {
		t.Fatalf("healthz = %d %q", rec.Code, rec.Body.String())
	}
}

func TestAPIRequiresSession(t *testing.T) {
	h := newHarness(t)
	if rec := h.do("GET", "/api/boards", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session = %d", rec.Code)
	}
	h.cookie = &http.Cookie{Name: auth.SessionCookie, Value: "forged.value"}
	if rec := h.do("GET", "/api/boards", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged session = %d", rec.Code)
	}
}

func TestOnlyAdminsCanSignIn(t *testing.T) {
	h := newHarness(t)
	rec := h.login("stranger@example.com")
	if rec.Code != http.StatusForbidden || h.cookie != nil {
		t.Fatalf("stranger sign-in = %d, cookie=%v", rec.Code, h.cookie != nil)
	}
	var n int
	h.srv.store.DB.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	if n != 0 {
		t.Fatal("refused sign-in should not create a user")
	}

	rec = h.login("admin@example.com")
	if rec.Code != http.StatusFound || h.cookie == nil {
		t.Fatalf("admin sign-in = %d", rec.Code)
	}
	if !h.cookie.Secure || !h.cookie.HttpOnly {
		t.Fatal("session cookie must be Secure and HttpOnly for an https base URL")
	}
	me := decode[map[string]any](t, h.do("GET", "/api/me", ""))
	if me["email"] != "admin@example.com" || me["admin"] != true {
		t.Fatalf("me = %v", me)
	}

	// Removing someone from WORKDESK_ADMIN_EMAILS locks out existing sessions.
	h.srv.cfg.AdminEmails = config.ParseEmails("other@example.com")
	if rec := h.do("GET", "/api/boards", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("de-listed admin = %d", rec.Code)
	}
}

func TestTodayFlow(t *testing.T) {
	h := newHarness(t)
	h.login("admin@example.com")

	boards := decode[[]db.Board](t, h.do("GET", "/api/boards", ""))
	if len(boards) != 2 || boards[0].Name != "Work" {
		t.Fatalf("boards = %+v", boards)
	}
	lists := decode[[]db.List](t, h.do("GET", "/api/boards/1/lists", ""))
	if len(lists) != 1 || lists[0].Name != "Today" {
		t.Fatalf("lists = %+v", lists)
	}
	itemsURL := "/api/lists/1/items"

	rec := h.do("POST", itemsURL, `{"title":"Plan sprint"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add = %d %s", rec.Code, rec.Body)
	}
	added := decode[[]db.Item](t, rec)

	rec = h.do("POST", itemsURL, `[{"title":"A","source_id":"x1"},{"title":"B","source_id":"x2"}]`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add many = %d %s", rec.Code, rec.Body)
	}
	h.do("POST", itemsURL, `[{"title":"A again","source_id":"x1"}]`)
	all := decode[[]db.Item](t, h.do("GET", itemsURL, ""))
	if len(all) != 3 {
		t.Fatalf("after idempotent repost: %d items, want 3", len(all))
	}

	rec = h.do("PATCH", "/api/items/"+itoa(added[0].ID), `{"done":true}`)
	if rec.Code != 200 || !decode[db.Item](t, rec).Done {
		t.Fatalf("check = %d %s", rec.Code, rec.Body)
	}
	open := decode[[]db.Item](t, h.do("GET", itemsURL+"?open=true", ""))
	if len(open) != 2 {
		t.Fatalf("open = %d, want 2", len(open))
	}
	// Checked today still shows in the full list (progress), not in open=true.
	all = decode[[]db.Item](t, h.do("GET", itemsURL, ""))
	if len(all) != 3 {
		t.Fatalf("all = %d, want 3", len(all))
	}
	rec = h.do("PATCH", "/api/items/"+itoa(added[0].ID), `{"done":false}`)
	if decode[db.Item](t, rec).Done {
		t.Fatal("uncheck failed")
	}
}

func TestAPIValidation(t *testing.T) {
	h := newHarness(t)
	h.login("admin@example.com")
	cases := []struct {
		method, url, body, ct string
		want                  int
	}{
		{"POST", "/api/lists/1/items", `{"title":""}`, "application/json", 400},
		{"POST", "/api/lists/1/items", `[]`, "application/json", 400},
		{"POST", "/api/lists/1/items", `title=x`, "application/x-www-form-urlencoded", 400},
		{"POST", "/api/lists/999/items", `{"title":"x"}`, "application/json", 404},
		{"PATCH", "/api/items/999", `{"done":true}`, "application/json", 404},
		{"GET", "/api/boards/abc/lists", ``, "", 404},
		{"GET", "/api/nope", ``, "", 404},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.url, strings.NewReader(c.body))
		if c.ct != "" {
			req.Header.Set("Content-Type", c.ct)
		}
		req.AddCookie(h.cookie)
		rec := httptest.NewRecorder()
		h.h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s %s %q = %d, want %d", c.method, c.url, c.body, rec.Code, c.want)
		}
	}
}

func TestCrossSiteWriteRejected(t *testing.T) {
	h := newHarness(t)
	h.login("admin@example.com")
	req := httptest.NewRequest("POST", "/api/lists/1/items", strings.NewReader(`{"title":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.AddCookie(h.cookie)
	rec := httptest.NewRecorder()
	h.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST = %d, want 403", rec.Code)
	}
}

func TestUIFallback(t *testing.T) {
	h := newHarness(t)
	for _, p := range []string{"/", "/boards/1", "/index.html"} {
		rec := h.do("GET", p, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>Workdesk") {
			t.Errorf("GET %s = %d", p, rec.Code)
		}
	}
	rec := h.do("GET", "/assets/app.js", "")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset = %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("CSP = %q", csp)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
