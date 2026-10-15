package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFeedbackCreatesIssue(t *testing.T) {
	var got ghIssue
	var auth, path string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"number": 7, "html_url": "https://github.com/o/r/issues/7"}`))
	}))
	defer gh.Close()

	h := newHarness(t)
	h.login("admin@example.com")
	if rec := h.do("POST", "/api/feedback", `{"kind":"bug","message":"x"}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured = %d", rec.Code)
	}
	h.srv.cfg.FeedbackToken, h.srv.cfg.FeedbackRepo, h.srv.cfg.GitHubAPI = "ghp_test", "o/r", gh.URL
	if me := decode[map[string]any](t, h.do("GET", "/api/me", "")); me["feedback"] != true {
		t.Fatalf("me.feedback = %v", me["feedback"])
	}

	rec := h.do("POST", "/api/feedback", `{"kind":"bug","message":"Remove button | overlaps\nsecond line","board":"Work","view":"desk","page":"/"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if out := decode[ghIssueOut](t, rec); out.Number != 7 || out.URL == "" {
		t.Fatalf("out = %+v", out)
	}
	if auth != "Bearer ghp_test" || path != "/repos/o/r/issues" {
		t.Fatalf("auth %q path %q", auth, path)
	}
	if got.Title != "[Bug] Remove button | overlaps" {
		t.Fatalf("title %q", got.Title)
	}
	for _, want := range []string{"second line", "Test (admin@example.com)", "Work · `/`", "`dev`"} {
		if !strings.Contains(got.Body, want) {
			t.Fatalf("body missing %q:\n%s", want, got.Body)
		}
	}
	if rec := h.do("POST", "/api/feedback", `{"kind":"nope","message":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad kind = %d", rec.Code)
	}
	if rec := h.do("POST", "/api/feedback", `{"kind":"idea","message":"  "}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty = %d", rec.Code)
	}
}

func TestFeedbackLimiter(t *testing.T) {
	var l feedbackLimiter
	now := time.Now()
	for i := 0; i < feedbackPerHour; i++ {
		if !l.allow(1, now) {
			t.Fatalf("send %d refused", i)
		}
	}
	if l.allow(1, now) {
		t.Fatal("over the limit allowed")
	}
	if !l.allow(2, now) || !l.allow(1, now.Add(61*time.Minute)) {
		t.Fatal("limit should be per user and per hour")
	}
}
