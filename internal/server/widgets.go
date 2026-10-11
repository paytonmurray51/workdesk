package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/paytonmurray51/workdesk/internal/db"
)

// inputError is a 400 with a message meant for the caller (often Claude), so it can fix the
// request and retry instead of guessing what "invalid input" meant.
type inputError string

func (e inputError) Error() string { return string(e) }

const jiraShape = `jira payload must be {"issues": [{"key": "SRE-123", "summary": "…", "status": "In Progress", ` +
	`"priority": "High", "due": "YYYY-MM-DD", "url": "https://…/browse/SRE-123"}]} (key and summary required)`

const calendarShape = `calendar payload must be {"date": "YYYY-MM-DD", "events": [{"title": "…", "start": "HH:MM", ` +
	`"end": "HH:MM", "location": "…", "url": "https://…", "id": "…"}]} (title and start required; 24h board-local times)`

// normalizeWidget checks the payload of the widgets the dashboard draws ("jira", "calendar")
// and rewrites common variants (Jira's own field layout, ISO datetimes, "items" instead of
// "issues"/"events") into the shape the tiles read. Other widget names pass through.
func normalizeWidget(name string, raw json.RawMessage, tz string) (json.RawMessage, error) {
	switch name {
	case "jira":
		return normalizeJira(raw)
	case "calendar":
		return normalizeCalendar(raw, tz)
	}
	return raw, nil
}

func normalizeJira(raw json.RawMessage) (json.RawMessage, error) {
	list, ok := findList(raw, "issues", "items", "tickets", "results")
	if !ok {
		return nil, inputError(jiraShape)
	}
	issues := make([]map[string]string, 0, len(list))
	for _, v := range list {
		m, _ := v.(map[string]any)
		key := firstStr(m, "key", "issue_key", "issueKey", "id")
		summary := firstStr(m, "summary", "title", "fields.summary")
		if key == "" || summary == "" {
			return nil, inputError(fmt.Sprintf("issue %d is missing key or summary. %s", len(issues)+1, jiraShape))
		}
		it := map[string]string{"key": key, "summary": summary}
		set(it, "status", firstStr(m, "status", "status.name", "fields.status.name"))
		set(it, "priority", firstStr(m, "priority", "priority.name", "fields.priority.name"))
		if d := firstStr(m, "due", "duedate", "due_date", "dueDate", "fields.duedate"); len(d) >= 10 && db.ValidDate(d[:10]) {
			it["due"] = d[:10]
		}
		set(it, "url", firstStr(m, "url", "link", "browse_url", "browseUrl", "web_url"))
		issues = append(issues, it)
	}
	return json.Marshal(map[string]any{"issues": issues})
}

var hhmmRe = regexp.MustCompile(`^([01]?[0-9]|2[0-3]):[0-5][0-9]$`)

func normalizeCalendar(raw json.RawMessage, tz string) (json.RawMessage, error) {
	list, ok := findList(raw, "events", "items", "meetings")
	if !ok {
		return nil, inputError(calendarShape)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	var top map[string]any
	json.Unmarshal(raw, &top)
	date := firstStr(top, "date")
	events := make([]map[string]string, 0, len(list))
	for _, v := range list {
		m, _ := v.(map[string]any)
		title := firstStr(m, "title", "summary", "subject", "name")
		start, sd, err1 := clock(firstStr(m, "start", "start_time", "startTime", "start.dateTime"), loc)
		end, _, err2 := clock(firstStr(m, "end", "end_time", "endTime", "end.dateTime"), loc)
		if title == "" || start == "" || err1 != nil || err2 != nil {
			return nil, inputError(fmt.Sprintf("event %d needs a title and a start time. %s", len(events)+1, calendarShape))
		}
		if date == "" && sd != "" {
			date = sd
		}
		ev := map[string]string{"title": title, "start": start}
		set(ev, "end", end)
		set(ev, "location", firstStr(m, "location", "location.displayName", "where"))
		set(ev, "url", firstStr(m, "url", "link", "webLink", "htmlLink", "join_url", "joinUrl"))
		set(ev, "id", firstStr(m, "id", "event_id", "eventId"))
		events = append(events, ev)
	}
	if date == "" {
		date = time.Now().In(loc).Format("2006-01-02")
	}
	if !db.ValidDate(date) {
		return nil, inputError(calendarShape)
	}
	return json.Marshal(map[string]any{"date": date, "events": events})
}

// clock turns "9:05", "09:05" or an ISO datetime into board-local "HH:MM" (and its date).
func clock(s string, loc *time.Location) (hm, date string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", nil
	}
	if hhmmRe.MatchString(s) {
		if len(s) == 4 {
			s = "0" + s
		}
		return s, "", nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		var t time.Time
		if strings.HasSuffix(layout, "Z07:00") {
			t, err = time.Parse(layout, s)
		} else {
			t, err = time.ParseInLocation(layout, s, loc)
		}
		if err == nil {
			t = t.In(loc)
			return t.Format("15:04"), t.Format("2006-01-02"), nil
		}
	}
	return "", "", fmt.Errorf("bad time %q", s)
}

// findList returns the array at the top level, or under the first of keys that holds one.
func findList(raw json.RawMessage, keys ...string) ([]any, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false
	}
	if a, ok := v.([]any); ok {
		return a, true
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	for _, k := range keys {
		if a, ok := m[k].([]any); ok {
			return a, true
		}
	}
	return nil, false
}

// firstStr returns the first non-empty string (or number) found at any dotted path.
func firstStr(m map[string]any, paths ...string) string {
	for _, p := range paths {
		var cur any = m
		for _, part := range strings.Split(p, ".") {
			mm, ok := cur.(map[string]any)
			if !ok {
				cur = nil
				break
			}
			cur = mm[part]
		}
		switch x := cur.(type) {
		case string:
			if s := strings.TrimSpace(x); s != "" {
				return s
			}
		case float64:
			return fmt.Sprintf("%v", x)
		}
	}
	return ""
}

func set(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}

// checkTimeboxes explains the first problem in a timebox proposal.
func checkTimeboxes(date string, boxes []db.NewTimebox) error {
	if !db.ValidDate(date) {
		return inputError(`date must be YYYY-MM-DD (the board's "today" from list_boards)`)
	}
	if len(boxes) > 100 {
		return inputError("at most 100 timeboxes per day")
	}
	for i, b := range boxes {
		n := i + 1
		switch {
		case !hhmmRe.MatchString(b.Start) || len(b.Start) != 5 || !hhmmRe.MatchString(b.End) || len(b.End) != 5:
			return inputError(fmt.Sprintf("timebox %d: start and end must be 24h HH:MM, e.g. 07:00 and 07:45", n))
		case b.Start >= b.End:
			return inputError(fmt.Sprintf("timebox %d: end (%s) must be after start (%s)", n, b.End, b.Start))
		case b.Kind != "task" && b.Kind != "meeting" && b.Kind != "break":
			return inputError(fmt.Sprintf(`timebox %d: kind must be "task", "meeting" or "break"`, n))
		case strings.TrimSpace(b.Title) == "" || len(b.Title) > 200:
			return inputError(fmt.Sprintf("timebox %d: title is required (max 200 characters)", n))
		}
	}
	return nil
}
