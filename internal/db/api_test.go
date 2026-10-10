package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAPITokens(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	u, _ := s.UpsertUser(ctx, "sub", "a@example.com", "A")

	tok, secret, err := s.CreateAPIToken(ctx, 1, u.ID, "Morning brief")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, APITokenPrefix) || len(secret) < 40 || !strings.HasPrefix(secret, tok.Prefix) {
		t.Fatalf("secret %q prefix %q", secret, tok.Prefix)
	}
	var stored string
	s.DB.QueryRow(`SELECT token_hash FROM api_tokens WHERE id = ?`, tok.ID).Scan(&stored)
	if stored == secret || stored != HashToken(secret) {
		t.Fatal("token must be stored hashed")
	}

	got, err := s.VerifyAPIToken(ctx, secret)
	if err != nil || got.BoardID != 1 || got.LastUsedAt == nil {
		t.Fatalf("verify = %+v, %v", got, err)
	}
	if _, err := s.VerifyAPIToken(ctx, secret+"x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong token err = %v", err)
	}
	if _, _, err := s.CreateAPIToken(ctx, 1, u.ID, " "); !errors.Is(err, ErrInvalid) {
		t.Fatalf("blank name err = %v", err)
	}

	if err := s.DeleteAPIToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyAPIToken(ctx, secret); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted token still verifies")
	}
}

func TestCreateListIdempotent(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	l, created, err := s.CreateList(ctx, 1, "Backlog", "")
	if err != nil || !created || l.Kind != "standing" {
		t.Fatalf("create = %+v %v %v", l, created, err)
	}
	again, created, err := s.CreateList(ctx, 1, "Backlog", "daily")
	if err != nil || created || again.ID != l.ID || again.Kind != "standing" {
		t.Fatalf("repeat = %+v %v %v", again, created, err)
	}
	if _, _, err := s.CreateList(ctx, 1, "X", "weekly"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad kind err = %v", err)
	}
	byName, err := s.ListByName(ctx, 1, "backlog")
	if err != nil || byName.ID != l.ID {
		t.Fatalf("ListByName = %+v %v", byName, err)
	}
}

func TestMoveItem(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	today := todayList(t, s, "Work")
	backlog, _, _ := s.CreateList(ctx, 1, "Backlog", "standing")
	s.AddItems(ctx, backlog.ID, []NewItem{{Title: "already there"}})
	items, _ := s.AddItems(ctx, today, []NewItem{{Title: "move me", SourceID: "J-1"}})

	moved, err := s.UpdateItem(ctx, items[0].ID, ItemPatch{ListID: &backlog.ID})
	if err != nil || moved.ListID != backlog.ID || moved.Position != 1 {
		t.Fatalf("moved = %+v, %v", moved, err)
	}
	// Moving onto a list that already has the same source_id is a conflict.
	s.AddItems(ctx, today, []NewItem{{Title: "dup", SourceID: "J-1"}})
	dup, _ := s.Items(ctx, today, true, time.Time{})
	if _, err := s.UpdateItem(ctx, dup[0].ID, ItemPatch{ListID: &backlog.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting move err = %v", err)
	}
}

func TestBriefings(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.LatestBriefing(ctx, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty latest err = %v", err)
	}
	setNow(t, time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC))
	b, err := s.PostBriefing(ctx, 1, NewBriefing{Markdown: "# Mon", SourceID: "brief-2026-10-12"})
	if err != nil || string(b.Stats) != "{}" {
		t.Fatalf("post = %+v %v", b, err)
	}
	setNow(t, time.Date(2026, 10, 12, 12, 5, 0, 0, time.UTC))
	again, err := s.PostBriefing(ctx, 1, NewBriefing{Markdown: "# Mon v2", Stats: json.RawMessage(`{"emails":4}`), SourceID: "brief-2026-10-12"})
	if err != nil || again.ID != b.ID || again.Markdown != "# Mon v2" || string(again.Stats) != `{"emails":4}` {
		t.Fatalf("repost = %+v %v", again, err)
	}
	setNow(t, time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC))
	s.PostBriefing(ctx, 1, NewBriefing{Markdown: "# Tue"})
	latest, _ := s.LatestBriefing(ctx, 1)
	if latest.Markdown != "# Tue" {
		t.Fatalf("latest = %q", latest.Markdown)
	}
	if _, err := s.PostBriefing(ctx, 1, NewBriefing{Markdown: "x", Stats: json.RawMessage(`[1]`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-object stats err = %v", err)
	}
	if _, err := s.PostBriefing(ctx, 1, NewBriefing{Markdown: "  "}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty markdown err = %v", err)
	}
}

func TestInboxKeepsStatusOnRepost(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	in, err := s.PushInbox(ctx, 1, []NewInboxItem{{Type: "email", Title: "Invoice", SourceID: "m-1"}, {Title: "No id"}})
	if err != nil || len(in) != 2 || in[0].Status != "new" {
		t.Fatalf("push = %+v %v", in, err)
	}
	s.DB.Exec(`UPDATE inbox_items SET status = 'dismissed' WHERE id = ?`, in[0].ID)
	again, _ := s.PushInbox(ctx, 1, []NewInboxItem{{Type: "email", Title: "Invoice (re)", SourceID: "m-1"}})
	if again[0].ID != in[0].ID || again[0].Status != "dismissed" || again[0].Title != "Invoice (re)" {
		t.Fatalf("repush = %+v", again[0])
	}
	newOnly, _ := s.Inbox(ctx, 1, "new")
	if len(newOnly) != 1 || newOnly[0].Title != "No id" {
		t.Fatalf("new inbox = %+v", newOnly)
	}
}

func TestWidgets(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.SetWidget(ctx, 1, "calendar", json.RawMessage(`{"events":[]}`)); err != nil {
		t.Fatal(err)
	}
	w, err := s.SetWidget(ctx, 1, "calendar", json.RawMessage(`{"events":[1]}`))
	if err != nil || string(w.Payload) != `{"events":[1]}` {
		t.Fatalf("replace = %+v %v", w, err)
	}
	all, _ := s.Widgets(ctx, 1)
	if len(all) != 1 {
		t.Fatalf("widgets = %d", len(all))
	}
	for _, bad := range []string{"Calendar", "a b", "", strings.Repeat("x", 65)} {
		if _, err := s.SetWidget(ctx, 1, bad, json.RawMessage(`{}`)); !errors.Is(err, ErrInvalid) {
			t.Errorf("name %q err = %v", bad, err)
		}
	}
	if _, err := s.SetWidget(ctx, 1, "jira", json.RawMessage(`{bad`)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad JSON err = %v", err)
	}
}

func TestTimeboxesReplaceDay(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	day := "2026-10-12"
	boxes, err := s.SetTimeboxes(ctx, 1, day, []NewTimebox{
		{Start: "09:00", End: "10:30", Kind: "task", Title: "Deep work", ItemIDs: []int64{3}},
		{Start: "07:00", End: "07:15", Kind: "meeting", Title: "Standup"},
	})
	if err != nil || len(boxes) != 2 || boxes[0].Title != "Standup" || boxes[1].ItemIDs[0] != 3 {
		t.Fatalf("set = %+v %v", boxes, err)
	}
	boxes, _ = s.SetTimeboxes(ctx, 1, day, []NewTimebox{{Start: "12:00", End: "13:00", Kind: "break", Title: "Lunch"}})
	if len(boxes) != 1 || boxes[0].Title != "Lunch" || boxes[0].Accepted {
		t.Fatalf("replace = %+v", boxes)
	}
	bad := [][]NewTimebox{
		{{Start: "10:00", End: "09:00", Kind: "task", Title: "backwards"}},
		{{Start: "9:00", End: "10:00", Kind: "task", Title: "short hour"}},
		{{Start: "09:00", End: "10:00", Kind: "nap", Title: "kind"}},
		{{Start: "09:00", End: "10:00", Kind: "task", Title: " "}},
	}
	for _, b := range bad {
		if _, err := s.SetTimeboxes(ctx, 1, day, b); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v err = %v", b, err)
		}
	}
	if _, err := s.SetTimeboxes(ctx, 1, "2026-13-01", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad date err = %v", err)
	}
	// Rejected writes leave the day alone.
	boxes, _ = s.Timeboxes(ctx, 1, day)
	if len(boxes) != 1 {
		t.Fatalf("after rejects: %d boxes", len(boxes))
	}
}
