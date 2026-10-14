package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid input")
)

const timeFmt = "2006-01-02T15:04:05Z"

// now is swapped in tests.
var now = func() time.Time { return time.Now().UTC() }

type User struct {
	ID        int64  `json:"id"`
	GoogleSub string `json:"-"`
	Email     string `json:"email"`
	Name      string `json:"name"`
}

type Board struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Timezone    string `json:"timezone"`
	HasSchedule bool   `json:"has_schedule"`
}

type List struct {
	ID      int64  `json:"id"`
	BoardID int64  `json:"board_id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
}

type Item struct {
	ID          int64   `json:"id"`
	ListID      int64   `json:"list_id"`
	Title       string  `json:"title"`
	Section     string  `json:"section"`
	Done        bool    `json:"done"`
	DoneAt      *string `json:"done_at"`
	Due         *string `json:"due"`
	CarriedDays int     `json:"carried_days"`
	SourceType  string  `json:"source_type"`
	SourceID    *string `json:"source_id"`
	SourceURL   string  `json:"source_url"`
	Position    int     `json:"position"`
}

// NewItem is the input for AddItems. A non-empty SourceID makes the post idempotent.
type NewItem struct {
	Title      string  `json:"title"`
	Section    string  `json:"section"`
	Due        *string `json:"due"`
	SourceType string  `json:"source_type"`
	SourceID   string  `json:"source_id"`
	SourceURL  string  `json:"source_url"`
}

// UpsertUser records a Google sign-in, keyed by the stable Google subject.
func (s *Store) UpsertUser(ctx context.Context, sub, email, name string) (*User, error) {
	u := &User{GoogleSub: sub}
	err := s.DB.QueryRowContext(ctx, `
		INSERT INTO users (google_sub, email, name) VALUES (?, ?, ?)
		ON CONFLICT(google_sub) DO UPDATE SET email = excluded.email, name = excluded.name
		RETURNING id, email, name`, sub, strings.ToLower(email), name).Scan(&u.ID, &u.Email, &u.Name)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	u := &User{}
	err := s.DB.QueryRowContext(ctx, `SELECT id, google_sub, email, name FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.GoogleSub, &u.Email, &u.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// Boards returns the boards a user can see. Admins see every board; others see the boards
// they are members of.
func (s *Store) Boards(ctx context.Context, userID int64, admin bool) ([]Board, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, name, color, timezone, has_schedule FROM boards
		WHERE ? OR id IN (SELECT board_id FROM board_members WHERE user_id = ?)
		ORDER BY position, id`, admin, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Board{}
	for rows.Next() {
		var b Board
		if err := rows.Scan(&b.ID, &b.Name, &b.Color, &b.Timezone, &b.HasSchedule); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Board returns one board if the user may see it, else ErrNotFound.
func (s *Store) Board(ctx context.Context, id, userID int64, admin bool) (*Board, error) {
	b := &Board{}
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, name, color, timezone, has_schedule FROM boards
		WHERE id = ? AND (? OR id IN (SELECT board_id FROM board_members WHERE user_id = ?))`,
		id, admin, userID).Scan(&b.ID, &b.Name, &b.Color, &b.Timezone, &b.HasSchedule)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

func (s *Store) Lists(ctx context.Context, boardID int64) ([]List, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, board_id, name, kind FROM lists WHERE board_id = ? ORDER BY position, id`, boardID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []List{}
	for rows.Next() {
		var l List
		if err := rows.Scan(&l.ID, &l.BoardID, &l.Name, &l.Kind); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) List(ctx context.Context, id int64) (*List, error) {
	l := &List{}
	err := s.DB.QueryRowContext(ctx, `SELECT id, board_id, name, kind FROM lists WHERE id = ?`, id).
		Scan(&l.ID, &l.BoardID, &l.Name, &l.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return l, err
}

const itemCols = `id, list_id, title, section, done, done_at, due, carried_days,
	source_type, source_id, source_url, position`

func scanItem(sc interface{ Scan(...any) error }) (Item, error) {
	var it Item
	err := sc.Scan(&it.ID, &it.ListID, &it.Title, &it.Section, &it.Done, &it.DoneAt, &it.Due,
		&it.CarriedDays, &it.SourceType, &it.SourceID, &it.SourceURL, &it.Position)
	return it, err
}

// Items returns a list's items. With openOnly, only unchecked items. Otherwise open items
// plus items checked at or after doneSince (e.g. the start of today), so a daily list
// shows today's progress without yesterday's finished work.
func (s *Store) Items(ctx context.Context, listID int64, openOnly bool, doneSince time.Time) ([]Item, error) {
	q := `SELECT ` + itemCols + ` FROM items WHERE list_id = ? AND (done = 0`
	args := []any{listID}
	if !openOnly {
		q += ` OR done_at >= ?`
		args = append(args, doneSince.UTC().Format(timeFmt))
	}
	q += `) ORDER BY position, id`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) Item(ctx context.Context, id int64) (*Item, error) {
	it, err := scanItem(s.DB.QueryRowContext(ctx, `SELECT `+itemCols+` FROM items WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &it, err
}

// AddItems inserts items at the end of the list in one transaction. Items with a source_id
// are upserted: a repeat post updates title/section/due/link but keeps done state and position.
// A source_id someone removed from the list (DeleteItem) is skipped and not returned.
func (s *Store) AddItems(ctx context.Context, listID int64, in []NewItem) ([]Item, error) {
	for i := range in {
		in[i].Title = strings.TrimSpace(in[i].Title)
		if in[i].Title == "" || len(in[i].Title) > 500 {
			return nil, ErrInvalid
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM lists WHERE id = ?`, listID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, ErrNotFound
	}
	ts := now().Format(timeFmt)
	out := make([]Item, 0, len(in))
	for _, n := range in {
		var sourceID *string
		if n.SourceID != "" {
			sourceID = &n.SourceID
			var removed int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM removed_items WHERE list_id = ? AND source_id = ?`,
				listID, n.SourceID).Scan(&removed); err != nil {
				return nil, err
			}
			if removed > 0 {
				continue
			}
		}
		row := tx.QueryRowContext(ctx, `
			INSERT INTO items (list_id, title, section, due, source_type, source_id, source_url, position, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, (SELECT coalesce(max(position), -1) + 1 FROM items WHERE list_id = ?), ?)
			ON CONFLICT(list_id, source_id) WHERE source_id IS NOT NULL DO UPDATE SET
				title = excluded.title, section = excluded.section, due = excluded.due,
				source_type = excluded.source_type, source_url = excluded.source_url,
				updated_at = excluded.updated_at
			RETURNING `+itemCols,
			listID, n.Title, n.Section, n.Due, n.SourceType, sourceID, n.SourceURL, listID, ts)
		it, err := scanItem(row)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, tx.Commit()
}

// ItemPatch holds optional changes; nil fields are left alone.
type ItemPatch struct {
	Done   *bool   `json:"done"`
	Title  *string `json:"title"`
	ListID *int64  `json:"list_id"` // move to another list (caller checks access)
}

// UpdateItem checks/unchecks, renames or moves an item. Checking sets done_at; unchecking
// clears it. A moved item goes to the end of the target list.
func (s *Store) UpdateItem(ctx context.Context, id int64, p ItemPatch) (*Item, error) {
	sets := []string{"updated_at = ?"}
	ts := now().Format(timeFmt)
	args := []any{ts}
	if p.Title != nil {
		t := strings.TrimSpace(*p.Title)
		if t == "" || len(t) > 500 {
			return nil, ErrInvalid
		}
		sets = append(sets, "title = ?")
		args = append(args, t)
	}
	if p.Done != nil {
		// Keep the original done_at if an already-checked item is checked again.
		if *p.Done {
			sets = append(sets, "done = 1", "done_at = CASE WHEN done = 1 THEN done_at ELSE ? END")
			args = append(args, ts)
		} else {
			sets = append(sets, "done = 0", "done_at = NULL")
		}
	}
	if p.ListID != nil {
		sets = append(sets, "position = CASE WHEN list_id = ? THEN position ELSE "+
			"(SELECT coalesce(max(position), -1) + 1 FROM items WHERE list_id = ?) END", "list_id = ?")
		args = append(args, *p.ListID, *p.ListID, *p.ListID)
	}
	args = append(args, id)
	it, err := scanItem(s.DB.QueryRowContext(ctx,
		`UPDATE items SET `+strings.Join(sets, ", ")+` WHERE id = ? RETURNING `+itemCols, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// DeleteItem removes one item.
// DeleteItem removes an item. If it came from a routine (source_id), that source_id is
// remembered for the list so a re-run doesn't add it back.
func (s *Store) DeleteItem(ctx context.Context, id int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var listID int64
	var sourceID *string
	err = tx.QueryRowContext(ctx, `DELETE FROM items WHERE id = ? RETURNING list_id, source_id`, id).Scan(&listID, &sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if sourceID != nil {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO removed_items (list_id, source_id, removed_at) VALUES (?, ?, ?)`,
			listID, *sourceID, now().Format(timeFmt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// StartOfDay returns local midnight of t in the named zone.
func StartOfDay(t time.Time, tz string) time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
}
