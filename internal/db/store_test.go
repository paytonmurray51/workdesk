package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func setNow(t *testing.T, ts time.Time) {
	t.Helper()
	old := now
	now = func() time.Time { return ts.UTC() }
	t.Cleanup(func() { now = old })
}

func todayList(t *testing.T, s *Store, board string) int64 {
	t.Helper()
	var id int64
	err := s.DB.QueryRow(`SELECT l.id FROM lists l JOIN boards b ON b.id = l.board_id
		WHERE b.name = ? AND l.name = 'Today'`, board).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOpenUsesWALAndSeeds(t *testing.T) {
	s := openTest(t)
	var mode string
	if err := s.DB.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}

	boards, err := s.Boards(context.Background(), 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(boards) != 2 || boards[0].Name != "Work" || boards[1].Name != "Personal" {
		t.Fatalf("boards = %+v", boards)
	}
	if !boards[0].HasSchedule || boards[1].HasSchedule {
		t.Fatalf("has_schedule: Work=%v Personal=%v", boards[0].HasSchedule, boards[1].HasSchedule)
	}
	for _, b := range boards {
		lists, err := s.Lists(context.Background(), b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(lists) != 1 || lists[0].Name != "Today" || lists[0].Kind != "daily" {
			t.Fatalf("board %s lists = %+v", b.Name, lists)
		}
	}
}

func TestMigrationsRunOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	for i := 0; i < 2; i++ {
		s, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		var n int
		s.DB.QueryRow(`SELECT count(*) FROM boards`).Scan(&n)
		s.Close()
		if n != 2 {
			t.Fatalf("open %d: %d boards, want 2 (seed ran twice?)", i, n)
		}
	}
}

func TestBoardAccess(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u, err := s.UpsertUser(ctx, "sub-1", "Someone@Example.com", "Someone")
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "someone@example.com" {
		t.Fatalf("email not lowercased: %q", u.Email)
	}
	boards, _ := s.Boards(ctx, u.ID, false)
	if len(boards) != 0 {
		t.Fatalf("non-member sees %d boards", len(boards))
	}
	if _, err := s.Board(ctx, 1, u.ID, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("non-member Board err = %v", err)
	}
	s.DB.Exec(`INSERT INTO board_members (board_id, user_id, role) VALUES (2, ?, 'viewer')`, u.ID)
	boards, _ = s.Boards(ctx, u.ID, false)
	if len(boards) != 1 || boards[0].Name != "Personal" {
		t.Fatalf("member boards = %+v", boards)
	}

	// Same Google subject signing in again updates rather than duplicates.
	u2, err := s.UpsertUser(ctx, "sub-1", "new@example.com", "New Name")
	if err != nil || u2.ID != u.ID || u2.Email != "new@example.com" {
		t.Fatalf("re-upsert = %+v, %v", u2, err)
	}
}

func TestAddCheckUncheck(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	list := todayList(t, s, "Work")
	t0 := time.Date(2026, 10, 10, 15, 0, 0, 0, time.UTC)
	setNow(t, t0)

	items, err := s.AddItems(ctx, list, []NewItem{{Title: "  Write report "}, {Title: "Call Sam"}})
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Title != "Write report" || items[0].Position != 0 || items[1].Position != 1 {
		t.Fatalf("items = %+v", items)
	}

	done := true
	it, err := s.UpdateItem(ctx, items[0].ID, ItemPatch{Done: &done})
	if err != nil {
		t.Fatal(err)
	}
	if !it.Done || it.DoneAt == nil || *it.DoneAt != "2026-10-10T15:00:00Z" {
		t.Fatalf("checked item = %+v", it)
	}

	// Checking again keeps the original done_at.
	setNow(t, t0.Add(time.Hour))
	it, _ = s.UpdateItem(ctx, items[0].ID, ItemPatch{Done: &done})
	if *it.DoneAt != "2026-10-10T15:00:00Z" {
		t.Fatalf("re-check moved done_at to %s", *it.DoneAt)
	}

	open, _ := s.Items(ctx, list, true, time.Time{})
	if len(open) != 1 || open[0].Title != "Call Sam" {
		t.Fatalf("open items = %+v", open)
	}

	undone := false
	it, err = s.UpdateItem(ctx, items[0].ID, ItemPatch{Done: &undone})
	if err != nil || it.Done || it.DoneAt != nil {
		t.Fatalf("unchecked item = %+v, %v", it, err)
	}

	title := "Write the report"
	it, _ = s.UpdateItem(ctx, items[0].ID, ItemPatch{Title: &title})
	if it.Title != title {
		t.Fatalf("rename: %q", it.Title)
	}
}

func TestItemsHidesEarlierDays(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	list := todayList(t, s, "Personal")
	done := true

	// Checked yesterday evening Central (01:00 UTC Oct 10 = 20:00 CDT Oct 9).
	setNow(t, time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC))
	old, _ := s.AddItems(ctx, list, []NewItem{{Title: "yesterday"}})
	s.UpdateItem(ctx, old[0].ID, ItemPatch{Done: &done})

	setNow(t, time.Date(2026, 10, 10, 14, 0, 0, 0, time.UTC))
	cur, _ := s.AddItems(ctx, list, []NewItem{{Title: "today done"}, {Title: "today open"}})
	s.UpdateItem(ctx, cur[0].ID, ItemPatch{Done: &done})

	since := StartOfDay(now(), "America/Chicago")
	if got := since.UTC().Format(timeFmt); got != "2026-10-10T05:00:00Z" {
		t.Fatalf("StartOfDay = %s", got)
	}
	items, err := s.Items(ctx, list, false, since)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, it := range items {
		titles = append(titles, it.Title)
	}
	if len(titles) != 2 || titles[0] != "today done" || titles[1] != "today open" {
		t.Fatalf("items = %v", titles)
	}
}

func TestAddItemsUpsertBySourceID(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	list := todayList(t, s, "Work")

	first, err := s.AddItems(ctx, list, []NewItem{
		{Title: "Review PR", SourceType: "jira", SourceID: "WD-1", SourceURL: "https://x/WD-1"},
		{Title: "No source"},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := true
	s.UpdateItem(ctx, first[0].ID, ItemPatch{Done: &done})

	again, err := s.AddItems(ctx, list, []NewItem{
		{Title: "Review PR (updated)", SourceType: "jira", SourceID: "WD-1", SourceURL: "https://x/WD-1"},
		{Title: "No source"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if again[0].ID != first[0].ID {
		t.Fatalf("source_id repost created new item %d (was %d)", again[0].ID, first[0].ID)
	}
	if again[0].Title != "Review PR (updated)" || !again[0].Done || again[0].Position != 0 {
		t.Fatalf("upserted item = %+v", again[0])
	}
	if again[1].ID == first[1].ID {
		t.Fatal("item without source_id should be inserted again")
	}

	var n int
	s.DB.QueryRow(`SELECT count(*) FROM items WHERE list_id = ?`, list).Scan(&n)
	if n != 3 {
		t.Fatalf("%d items, want 3", n)
	}

	// The same source_id on another list is a different item.
	other := todayList(t, s, "Personal")
	o, _ := s.AddItems(ctx, other, []NewItem{{Title: "x", SourceID: "WD-1"}})
	if o[0].ID == first[0].ID {
		t.Fatal("source_id should be scoped per list")
	}
}

func TestAddItemsValidation(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	list := todayList(t, s, "Work")

	if _, err := s.AddItems(ctx, list, []NewItem{{Title: "ok"}, {Title: "   "}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank title err = %v", err)
	}
	items, _ := s.Items(ctx, list, true, time.Time{})
	if len(items) != 0 {
		t.Fatal("a rejected batch must not insert anything")
	}
	if _, err := s.AddItems(ctx, 9999, []NewItem{{Title: "x"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing list err = %v", err)
	}
	if _, err := s.UpdateItem(ctx, 9999, ItemPatch{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing item err = %v", err)
	}
}
