package db

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

var ErrConflict = errors.New("conflict")

// HashToken is how every bearer secret (API token, OAuth code/token) is stored.
// Tokens are 256-bit random values, so a plain SHA-256 is enough.
func HashToken(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// NewSecret returns prefix + 43 random base64url characters (256 bits).
func NewSecret(prefix string) string {
	b := make([]byte, 32)
	rand.Read(b)
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

const APITokenPrefix = "wdk_"

type APIToken struct {
	ID         int64   `json:"id"`
	BoardID    int64   `json:"board_id"`
	Name       string  `json:"name"`
	Prefix     string  `json:"prefix"`
	CreatedAt  string  `json:"created_at"`
	LastUsedAt *string `json:"last_used_at"`
}

// CreateAPIToken makes a board-scoped token. The secret is returned once and only its hash kept.
func (s *Store) CreateAPIToken(ctx context.Context, boardID, userID int64, name string) (*APIToken, string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return nil, "", ErrInvalid
	}
	secret := NewSecret(APITokenPrefix)
	t := &APIToken{}
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO api_tokens (board_id, name, token_hash, prefix, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		RETURNING id, board_id, name, prefix, created_at, last_used_at`,
		boardID, name, HashToken(secret), secret[:len(APITokenPrefix)+6], userID, now().Format(timeFmt)).
		Scan(&t.ID, &t.BoardID, &t.Name, &t.Prefix, &t.CreatedAt, &t.LastUsedAt)
	if err != nil {
		return nil, "", err
	}
	return t, secret, nil
}

func (s *Store) APITokens(ctx context.Context, boardID int64) ([]APIToken, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, board_id, name, prefix, created_at, last_used_at FROM api_tokens
		WHERE board_id = ? ORDER BY id`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIToken{}
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.BoardID, &t.Name, &t.Prefix, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// APIToken returns one token's metadata (used to check access before deleting).
func (s *Store) APIToken(ctx context.Context, id int64) (*APIToken, error) {
	t := &APIToken{}
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, board_id, name, prefix, created_at, last_used_at FROM api_tokens WHERE id = ?`, id).
		Scan(&t.ID, &t.BoardID, &t.Name, &t.Prefix, &t.CreatedAt, &t.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (s *Store) DeleteAPIToken(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// VerifyAPIToken returns the board a token belongs to and records its use.
func (s *Store) VerifyAPIToken(ctx context.Context, secret string) (*APIToken, error) {
	t := &APIToken{}
	err := s.DB.QueryRowContext(ctx, `
		UPDATE api_tokens SET last_used_at = ? WHERE token_hash = ?
		RETURNING id, board_id, name, prefix, created_at, last_used_at`,
		now().Format(timeFmt), HashToken(secret)).
		Scan(&t.ID, &t.BoardID, &t.Name, &t.Prefix, &t.CreatedAt, &t.LastUsedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// BoardByID returns a board without any access check.
func (s *Store) BoardByID(ctx context.Context, id int64) (*Board, error) {
	b := &Board{}
	err := s.DB.QueryRowContext(ctx, `SELECT id, name, color, timezone, has_schedule FROM boards WHERE id = ?`, id).
		Scan(&b.ID, &b.Name, &b.Color, &b.Timezone, &b.HasSchedule)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

// AllBoards lists every board (callers filter by access).
func (s *Store) AllBoards(ctx context.Context) ([]Board, error) { return s.Boards(ctx, 0, true) }

// CreateList adds a list to a board. It is idempotent by name: if the board already has a
// list with that name, the existing list is returned with created=false.
func (s *Store) CreateList(ctx context.Context, boardID int64, name, kind string) (*List, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return nil, false, ErrInvalid
	}
	if kind == "" {
		kind = "standing"
	}
	if kind != "daily" && kind != "standing" {
		return nil, false, ErrInvalid
	}
	res, err := s.DB.ExecContext(ctx, `
		INSERT INTO lists (board_id, name, kind, position)
		VALUES (?, ?, ?, (SELECT coalesce(max(position), -1) + 1 FROM lists WHERE board_id = ?))
		ON CONFLICT(board_id, name) DO NOTHING`, boardID, name, kind, boardID)
	if err != nil {
		return nil, false, err
	}
	n, _ := res.RowsAffected()
	l := &List{}
	err = s.DB.QueryRowContext(ctx, `SELECT id, board_id, name, kind FROM lists WHERE board_id = ? AND name = ?`,
		boardID, name).Scan(&l.ID, &l.BoardID, &l.Name, &l.Kind)
	return l, n > 0, err
}

// ListByName finds a board's list by case-insensitive name.
func (s *Store) ListByName(ctx context.Context, boardID int64, name string) (*List, error) {
	l := &List{}
	err := s.DB.QueryRowContext(ctx, `SELECT id, board_id, name, kind FROM lists
		WHERE board_id = ? AND name = ? COLLATE NOCASE`, boardID, strings.TrimSpace(name)).
		Scan(&l.ID, &l.BoardID, &l.Name, &l.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return l, err
}

// Briefings

type Briefing struct {
	ID        int64           `json:"id"`
	BoardID   int64           `json:"board_id"`
	Markdown  string          `json:"markdown"`
	Stats     json.RawMessage `json:"stats"`
	SourceID  *string         `json:"source_id"`
	CreatedAt string          `json:"created_at"`
	UpdatedAt string          `json:"updated_at"`
}

type NewBriefing struct {
	Markdown string          `json:"markdown"`
	Stats    json.RawMessage `json:"stats,omitempty"`
	SourceID string          `json:"source_id,omitempty"`
}

const maxMarkdown = 64 << 10

// PostBriefing stores a briefing; a repeat post with the same source_id replaces it.
func (s *Store) PostBriefing(ctx context.Context, boardID int64, in NewBriefing) (*Briefing, error) {
	if strings.TrimSpace(in.Markdown) == "" || len(in.Markdown) > maxMarkdown {
		return nil, ErrInvalid
	}
	stats, err := normalizeObject(in.Stats, 16<<10)
	if err != nil {
		return nil, err
	}
	ts := now().Format(timeFmt)
	b := &Briefing{}
	var statsText string
	err = s.DB.QueryRowContext(ctx, `
		INSERT INTO briefings (board_id, markdown, stats, source_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(board_id, source_id) WHERE source_id IS NOT NULL DO UPDATE SET
			markdown = excluded.markdown, stats = excluded.stats, updated_at = excluded.updated_at
		RETURNING id, board_id, markdown, stats, source_id, created_at, updated_at`,
		boardID, in.Markdown, stats, nullable(in.SourceID), ts, ts).
		Scan(&b.ID, &b.BoardID, &b.Markdown, &statsText, &b.SourceID, &b.CreatedAt, &b.UpdatedAt)
	if err != nil {
		return nil, err
	}
	b.Stats = json.RawMessage(statsText)
	return b, nil
}

// LatestBriefing returns the most recently created briefing, or ErrNotFound.
func (s *Store) LatestBriefing(ctx context.Context, boardID int64) (*Briefing, error) {
	b := &Briefing{}
	var statsText string
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, board_id, markdown, stats, source_id, created_at, updated_at FROM briefings
		WHERE board_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`, boardID).
		Scan(&b.ID, &b.BoardID, &b.Markdown, &statsText, &b.SourceID, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	b.Stats = json.RawMessage(statsText)
	return b, err
}

// Inbox

type InboxItem struct {
	ID        int64   `json:"id"`
	BoardID   int64   `json:"board_id"`
	Type      string  `json:"type"`
	Title     string  `json:"title"`
	Snippet   string  `json:"snippet"`
	URL       string  `json:"url"`
	Reason    string  `json:"reason"`
	Status    string  `json:"status"`
	SourceID  *string `json:"source_id"`
	CreatedAt string  `json:"created_at"`
	// SnoozedUntil is set while status is "snoozed"; the item returns as "new" after it.
	SnoozedUntil *string `json:"snoozed_until"`
}

type NewInboxItem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Snippet  string `json:"snippet"`
	URL      string `json:"url"`
	Reason   string `json:"reason"`
	SourceID string `json:"source_id"`
}

const inboxCols = `id, board_id, type, title, snippet, url, reason, status, source_id, created_at, snoozed_until`

func scanInbox(sc interface{ Scan(...any) error }) (InboxItem, error) {
	var it InboxItem
	err := sc.Scan(&it.ID, &it.BoardID, &it.Type, &it.Title, &it.Snippet, &it.URL, &it.Reason,
		&it.Status, &it.SourceID, &it.CreatedAt, &it.SnoozedUntil)
	return it, err
}

// PushInbox adds triage candidates. Re-pushing a source_id refreshes the text but keeps
// the status, so something already dismissed stays dismissed.
func (s *Store) PushInbox(ctx context.Context, boardID int64, in []NewInboxItem) ([]InboxItem, error) {
	for i := range in {
		in[i].Title = strings.TrimSpace(in[i].Title)
		if in[i].Title == "" || len(in[i].Title) > 500 || len(in[i].Snippet) > 4000 {
			return nil, ErrInvalid
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ts := now().Format(timeFmt)
	out := make([]InboxItem, 0, len(in))
	for _, n := range in {
		it, err := scanInbox(tx.QueryRowContext(ctx, `
			INSERT INTO inbox_items (board_id, type, title, snippet, url, reason, source_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(board_id, source_id) WHERE source_id IS NOT NULL DO UPDATE SET
				type = excluded.type, title = excluded.title, snippet = excluded.snippet,
				url = excluded.url, reason = excluded.reason, updated_at = excluded.updated_at
			RETURNING `+inboxCols,
			boardID, n.Type, n.Title, n.Snippet, n.URL, n.Reason, nullable(n.SourceID), ts, ts))
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, tx.Commit()
}

// Inbox lists a board's inbox items, optionally filtered by status, newest first.
func (s *Store) Inbox(ctx context.Context, boardID int64, status string) ([]InboxItem, error) {
	// Wake snoozed items whose time has come.
	if _, err := s.DB.ExecContext(ctx, `UPDATE inbox_items SET status = 'new', snoozed_until = NULL
		WHERE board_id = ? AND status = 'snoozed' AND snoozed_until <= ?`, boardID, now().Format(timeFmt)); err != nil {
		return nil, err
	}
	q := `SELECT ` + inboxCols + ` FROM inbox_items WHERE board_id = ?`
	args := []any{boardID}
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY created_at DESC, id DESC LIMIT 200`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InboxItem{}
	for rows.Next() {
		it, err := scanInbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) InboxItem(ctx context.Context, id int64) (*InboxItem, error) {
	it, err := scanInbox(s.DB.QueryRowContext(ctx, `SELECT `+inboxCols+` FROM inbox_items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &it, err
}

// SetInboxStatus triages an inbox item. "snoozed" needs a future until; other statuses clear it.
func (s *Store) SetInboxStatus(ctx context.Context, id int64, status string, until time.Time) (*InboxItem, error) {
	var snooze *string
	switch status {
	case "snoozed":
		if !until.After(now()) {
			return nil, ErrInvalid
		}
		u := until.UTC().Format(timeFmt)
		snooze = &u
	case "new", "added", "dismissed":
	default:
		return nil, ErrInvalid
	}
	it, err := scanInbox(s.DB.QueryRowContext(ctx, `
		UPDATE inbox_items SET status = ?, snoozed_until = ?, updated_at = ? WHERE id = ?
		RETURNING `+inboxCols, status, snooze, now().Format(timeFmt), id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &it, err
}

// InboxToList turns an inbox entry into a to-do on a list (keeping its source link, so a
// later post of the same source_id updates that to-do) and marks the entry "added".
func (s *Store) InboxToList(ctx context.Context, inboxID, listID int64) (*Item, error) {
	in, err := s.InboxItem(ctx, inboxID)
	if err != nil {
		return nil, err
	}
	n := NewItem{Title: in.Title, SourceType: in.Type, SourceURL: in.URL}
	if in.SourceID != nil {
		n.SourceID = *in.SourceID
	}
	items, err := s.AddItems(ctx, listID, []NewItem{n})
	if err != nil {
		return nil, err
	}
	if _, err := s.SetInboxStatus(ctx, inboxID, "added", time.Time{}); err != nil {
		return nil, err
	}
	return &items[0], nil
}

// ClearCounts reports how many rows ClearBoard removed.
type ClearCounts struct {
	Items     int64 `json:"items"`
	Briefings int64 `json:"briefings"`
	Inbox     int64 `json:"inbox"`
	Widgets   int64 `json:"widgets"`
	Timeboxes int64 `json:"timeboxes"`
}

// ClearBoard erases everything posted to a board: list items, briefings, inbox, widgets and
// timeboxes. The board, its lists, API tokens and OAuth grants stay. There is no undo.
func (s *Store) ClearBoard(ctx context.Context, boardID int64) (*ClearCounts, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c := &ClearCounts{}
	for _, q := range []struct {
		sql string
		n   *int64
	}{
		{`DELETE FROM items WHERE list_id IN (SELECT id FROM lists WHERE board_id = ?)`, &c.Items},
		{`DELETE FROM briefings WHERE board_id = ?`, &c.Briefings},
		{`DELETE FROM inbox_items WHERE board_id = ?`, &c.Inbox},
		{`DELETE FROM widgets WHERE board_id = ?`, &c.Widgets},
		{`DELETE FROM timeboxes WHERE board_id = ?`, &c.Timeboxes},
	} {
		res, err := tx.ExecContext(ctx, q.sql, boardID)
		if err != nil {
			return nil, err
		}
		*q.n, _ = res.RowsAffected()
	}
	return c, tx.Commit()
}

// Widgets

type Widget struct {
	Name      string          `json:"name"`
	Payload   json.RawMessage `json:"payload"`
	UpdatedAt string          `json:"updated_at"`
}

var widgetName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// SetWidget replaces a named widget's JSON payload (e.g. "calendar", "jira").
func (s *Store) SetWidget(ctx context.Context, boardID int64, name string, payload json.RawMessage) (*Widget, error) {
	if !widgetName.MatchString(name) || len(payload) == 0 || len(payload) > 256<<10 || !json.Valid(payload) {
		return nil, ErrInvalid
	}
	w := &Widget{Name: name}
	var p string
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO widgets (board_id, name, payload, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(board_id, name) DO UPDATE SET payload = excluded.payload, updated_at = excluded.updated_at
		RETURNING payload, updated_at`, boardID, name, string(payload), now().Format(timeFmt)).
		Scan(&p, &w.UpdatedAt)
	if err != nil {
		return nil, err
	}
	w.Payload = json.RawMessage(p)
	return w, nil
}

func (s *Store) Widgets(ctx context.Context, boardID int64) ([]Widget, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT name, payload, updated_at FROM widgets WHERE board_id = ? ORDER BY name`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Widget{}
	for rows.Next() {
		var w Widget
		var p string
		if err := rows.Scan(&w.Name, &p, &w.UpdatedAt); err != nil {
			return nil, err
		}
		w.Payload = json.RawMessage(p)
		out = append(out, w)
	}
	return out, rows.Err()
}

// Timeboxes

type Timebox struct {
	ID       int64   `json:"id"`
	Date     string  `json:"date"`
	Start    string  `json:"start"`
	End      string  `json:"end"`
	Kind     string  `json:"kind"`
	Title    string  `json:"title"`
	ItemIDs  []int64 `json:"item_ids"`
	Accepted bool    `json:"accepted"`
}

type NewTimebox struct {
	Start   string  `json:"start"`
	End     string  `json:"end"`
	Kind    string  `json:"kind"`
	Title   string  `json:"title"`
	ItemIDs []int64 `json:"item_ids"`
}

var hhmm = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// ValidDate reports whether d is a YYYY-MM-DD calendar date.
func ValidDate(d string) bool {
	_, err := time.Parse("2006-01-02", d)
	return err == nil
}

// SetTimeboxes replaces the proposed boxes for one day. Proposals start un-accepted.
func (s *Store) SetTimeboxes(ctx context.Context, boardID int64, date string, in []NewTimebox) ([]Timebox, error) {
	if !ValidDate(date) || len(in) > 100 {
		return nil, ErrInvalid
	}
	for _, b := range in {
		if !hhmm.MatchString(b.Start) || !hhmm.MatchString(b.End) || b.Start >= b.End ||
			(b.Kind != "task" && b.Kind != "meeting" && b.Kind != "break") ||
			strings.TrimSpace(b.Title) == "" || len(b.Title) > 200 {
			return nil, ErrInvalid
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM timeboxes WHERE board_id = ? AND date = ?`, boardID, date); err != nil {
		return nil, err
	}
	for _, b := range in {
		ids := b.ItemIDs
		if ids == nil {
			ids = []int64{}
		}
		j, _ := json.Marshal(ids)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO timeboxes (board_id, date, start, "end", kind, title, item_ids) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			boardID, date, b.Start, b.End, b.Kind, strings.TrimSpace(b.Title), string(j)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.Timeboxes(ctx, boardID, date)
}

func (s *Store) Timeboxes(ctx context.Context, boardID int64, date string) ([]Timebox, error) {
	if !ValidDate(date) {
		return nil, ErrInvalid
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, date, start, "end", kind, title, item_ids, accepted FROM timeboxes
		WHERE board_id = ? AND date = ? ORDER BY start, id`, boardID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Timebox{}
	for rows.Next() {
		var b Timebox
		var ids string
		if err := rows.Scan(&b.ID, &b.Date, &b.Start, &b.End, &b.Kind, &b.Title, &ids, &b.Accepted); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(ids), &b.ItemIDs)
		out = append(out, b)
	}
	return out, rows.Err()
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// normalizeObject validates an optional JSON object, defaulting to {}.
func normalizeObject(raw json.RawMessage, max int) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}", nil
	}
	if len(raw) > max {
		return "", ErrInvalid
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", ErrInvalid
	}
	return string(raw), nil
}

// AcceptTimeboxes marks every box of one day as accepted (or back to proposed).
func (s *Store) AcceptTimeboxes(ctx context.Context, boardID int64, date string, accepted bool) ([]Timebox, error) {
	if !ValidDate(date) {
		return nil, ErrInvalid
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE timeboxes SET accepted = ? WHERE board_id = ? AND date = ?`,
		accepted, boardID, date); err != nil {
		return nil, err
	}
	return s.Timeboxes(ctx, boardID, date)
}

// RollOver runs a board's carry-over once per local day. On the first call for a new day it
// adds one to carried_days of every open item on the board's daily lists that was created
// before today; countDay=false (e.g. weekends on a work board) records the day without
// counting it. It returns how many items were bumped. Calling it again the same day is a no-op.
func (s *Store) RollOver(ctx context.Context, boardID int64, today string, startOfToday time.Time, countDay bool) (int64, error) {
	if !ValidDate(today) {
		return 0, ErrInvalid
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var last sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT rolled_over_on FROM boards WHERE id = ?`, boardID).Scan(&last); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if last.Valid && last.String >= today {
		return 0, nil
	}
	var n int64
	// The first run only records the day: there is no "yesterday" to carry from yet.
	if last.Valid && countDay {
		res, err := tx.ExecContext(ctx, `
			UPDATE items SET carried_days = carried_days + 1, updated_at = ?
			WHERE done = 0 AND created_at < ?
			  AND list_id IN (SELECT id FROM lists WHERE board_id = ? AND kind = 'daily')`,
			now().Format(timeFmt), startOfToday.UTC().Format(timeFmt), boardID)
		if err != nil {
			return 0, err
		}
		n, _ = res.RowsAffected()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE boards SET rolled_over_on = ? WHERE id = ?`, today, boardID); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}
