package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Version is the running build (git commit), set at build time with
// -ldflags "-X github.com/paytonmurray51/workdesk/internal/server.Version=...".
var Version = "dev"

type feedbackIn struct {
	Kind    string `json:"kind"`    // bug, idea or other
	Message string `json:"message"` // what the person typed
	Board   string `json:"board"`   // board name they were on
	View    string `json:"view"`    // "desk" or "settings"
	Page    string `json:"page"`    // location path + hash
}

var feedbackKinds = map[string]string{"bug": "Bug", "idea": "Idea", "other": "Feedback"}

// feedbackLimiter allows a few submissions per user per hour, so a stuck button or a script
// can't flood the repo with issues.
type feedbackLimiter struct {
	mu   sync.Mutex
	sent map[int64][]time.Time
}

const feedbackPerHour = 10

func (l *feedbackLimiter) allow(userID int64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sent == nil {
		l.sent = map[int64][]time.Time{}
	}
	recent := l.sent[userID][:0]
	for _, t := range l.sent[userID] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= feedbackPerHour {
		l.sent[userID] = recent
		return false
	}
	l.sent[userID] = append(recent, now)
	return true
}

// postFeedback files the message as a GitHub issue. Only signed-in people (not API tokens or
// connectors) can send feedback.
func (s *Server) postFeedback(w http.ResponseWriter, r *http.Request, p *principal) {
	if p.user == nil {
		writeErr(w, http.StatusForbidden, "feedback is for signed-in people")
		return
	}
	if s.cfg.FeedbackToken == "" {
		writeErr(w, http.StatusServiceUnavailable, "feedback isn't set up on this server")
		return
	}
	var in feedbackIn
	if !decodeBody(w, r, &in) {
		return
	}
	in.Message = strings.TrimSpace(in.Message)
	kind, ok := feedbackKinds[in.Kind]
	switch {
	case !ok:
		writeErr(w, http.StatusBadRequest, `kind must be "bug", "idea" or "other"`)
		return
	case in.Message == "" || len(in.Message) > 5000:
		writeErr(w, http.StatusBadRequest, "message is required (max 5000 characters)")
		return
	}
	if !s.feedback.allow(p.user.ID, time.Now()) {
		writeErr(w, http.StatusTooManyRequests, "that's a lot of feedback for one hour; try again later")
		return
	}
	issue, err := s.createIssue(r.Context(), feedbackIssue(kind, in, p, Version))
	if err != nil {
		writeErr(w, http.StatusBadGateway, "couldn't create the GitHub issue: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, issue)
}

type ghIssue struct {
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Labels []string `json:"labels"`
}

type ghIssueOut struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

func feedbackIssue(kind string, in feedbackIn, p *principal, version string) ghIssue {
	title := strings.TrimSpace(strings.SplitN(in.Message, "\n", 2)[0])
	if r := []rune(title); len(r) > 70 {
		title = string(r[:69]) + "…"
	}
	who := p.user.Email
	if p.user.Name != "" {
		who = fmt.Sprintf("%s (%s)", p.user.Name, p.user.Email)
	}
	where := in.Board
	if in.View == "settings" {
		where = "Settings"
	}
	if where == "" {
		where = "—"
	}
	page := in.Page
	if page == "" {
		page = "/"
	}
	var b strings.Builder
	b.WriteString(in.Message)
	b.WriteString("\n\n---\n")
	fmt.Fprintf(&b, "| | |\n|---|---|\n| From | %s |\n| Board / page | %s · `%s` |\n| Version | `%s` |\n",
		cell(who), cell(where), strings.ReplaceAll(page, "`", ""), strings.ReplaceAll(version, "`", ""))
	b.WriteString("\n_Sent with the Feedback button in Workdesk._\n")
	return ghIssue{Title: fmt.Sprintf("[%s] %s", kind, title), Body: b.String(),
		Labels: []string{"feedback", strings.ToLower(kind)}}
}

// cell keeps user-supplied text from breaking the Markdown table.
func cell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ", "\r", "").Replace(s)
}

func (s *Server) createIssue(ctx context.Context, issue ghIssue) (*ghIssueOut, error) {
	body, _ := json.Marshal(issue)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.cfg.GitHubAPI+"/repos/"+s.cfg.FeedbackRepo+"/issues", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.FeedbackToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("GitHub answered %s", res.Status)
	}
	var out struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &ghIssueOut{Number: out.Number, URL: out.HTMLURL}, nil
}
