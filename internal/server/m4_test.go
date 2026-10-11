package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/paytonmurray51/workdesk/internal/db"
)

func TestJiraWidgetNormalizes(t *testing.T) {
	cases := map[string]string{
		"canonical": `{"issues":[{"key":"SRE-1","summary":"Fix it","status":"In Progress","due":"2026-10-12","url":"https://x/browse/SRE-1"}]}`,
		"jira api":  `{"issues":[{"key":"SRE-1","fields":{"summary":"Fix it","status":{"name":"In Progress"},"priority":{"name":"High"},"duedate":"2026-10-12"}}]}`,
		"items":     `{"items":[{"key":"SRE-1","title":"Fix it","status":"In Progress"}]}`,
		"array":     `[{"key":"SRE-1","summary":"Fix it"}]`,
	}
	for name, in := range cases {
		out, err := normalizeWidget("jira", json.RawMessage(in), "America/Chicago")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var p struct {
			Issues []map[string]string `json:"issues"`
		}
		json.Unmarshal(out, &p)
		if len(p.Issues) != 1 || p.Issues[0]["key"] != "SRE-1" || p.Issues[0]["summary"] != "Fix it" {
			t.Fatalf("%s: %s", name, out)
		}
	}
	for _, bad := range []string{`{"tickets_count":3}`, `{"issues":[{"summary":"no key"}]}`, `"x"`} {
		if _, err := normalizeWidget("jira", json.RawMessage(bad), "UTC"); err == nil || !strings.Contains(err.Error(), "issues") {
			t.Fatalf("bad %s: err = %v", bad, err)
		}
	}
}

func TestCalendarWidgetConvertsISOTimes(t *testing.T) {
	in := `{"events":[{"subject":"Standup","start":"2026-10-12T14:00:00Z","end":"2026-10-12T14:15:00Z","webLink":"https://o/1"},
		{"title":"Lunch","start":"12:00","end":"13:00"}]}`
	out, err := normalizeWidget("calendar", json.RawMessage(in), "America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Date   string              `json:"date"`
		Events []map[string]string `json:"events"`
	}
	json.Unmarshal(out, &p)
	if p.Date != "2026-10-12" || p.Events[0]["start"] != "09:00" || p.Events[0]["end"] != "09:15" ||
		p.Events[0]["title"] != "Standup" || p.Events[0]["url"] != "https://o/1" || p.Events[1]["start"] != "12:00" {
		t.Fatalf("calendar = %s", out)
	}
	if _, err := normalizeWidget("calendar", json.RawMessage(`{"events":[{"title":"x"}]}`), "UTC"); err == nil {
		t.Fatal("event without start accepted")
	}
	if out, err := normalizeWidget("notes", json.RawMessage(`{"any":1}`), "UTC"); err != nil || string(out) != `{"any":1}` {
		t.Fatalf("other widgets pass through: %s %v", out, err)
	}
}

func TestWidgetAndTimeboxErrorsExplainThemselves(t *testing.T) {
	h := newHarness(t)
	h.login("admin@example.com")
	tok := h.newToken("1", "routine")

	rec := h.bearer(tok, "PUT", "/api/boards/1/widgets/jira", `{"count":2}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "key and summary required") {
		t.Fatalf("bad jira = %d %s", rec.Code, rec.Body)
	}
	rec = h.bearer(tok, "PUT", "/api/boards/1/widgets/jira", `{"issues":[{"key":"SRE-3364","fields":{"summary":"Baseline SQL"}}]}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"summary":"Baseline SQL"`) {
		t.Fatalf("jira api shape = %d %s", rec.Code, rec.Body)
	}

	rec = h.bearer(tok, "PUT", "/api/boards/1/days/2026-10-12/timeboxes", `[{"start":"9:00","end":"10:00","kind":"task","title":"x"}]`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "HH:MM") {
		t.Fatalf("bad timebox = %d %s", rec.Code, rec.Body)
	}
	rec = h.bearer(tok, "PUT", "/api/boards/1/days/2026-10-12/timeboxes", `[{"start":"10:00","end":"09:00","kind":"task","title":"x"}]`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "after start") {
		t.Fatalf("backwards timebox = %d %s", rec.Code, rec.Body)
	}
}

func TestAcceptTimeboxesAPI(t *testing.T) {
	h := newHarness(t)
	h.login("admin@example.com")
	h.do("PUT", "/api/boards/1/days/2026-10-12/timeboxes",
		`[{"start":"07:00","end":"07:15","kind":"meeting","title":"Standup"},{"start":"07:30","end":"09:00","kind":"task","title":"Focus"}]`)
	rec := h.do("PATCH", "/api/boards/1/days/2026-10-12/timeboxes", `{"accepted":true}`)
	boxes := decode[[]db.Timebox](t, rec)
	if rec.Code != 200 || len(boxes) != 2 || !boxes[0].Accepted || !boxes[1].Accepted {
		t.Fatalf("accept = %d %+v", rec.Code, boxes)
	}
	if rec := h.do("PATCH", "/api/boards/1/days/2026-10-12/timeboxes", `{}`); rec.Code != 400 {
		t.Fatalf("missing accepted = %d", rec.Code)
	}
}
