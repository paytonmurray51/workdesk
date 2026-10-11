package db

import (
	"context"
	"testing"
	"time"
)

func TestRollOverCountsOncePerDay(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	list := todayList(t, s, "Work")
	items, err := s.AddItems(ctx, list, []NewItem{{Title: "open"}, {Title: "finished"}})
	if err != nil {
		t.Fatal(err)
	}
	done := true
	if _, err := s.UpdateItem(ctx, items[1].ID, ItemPatch{Done: &done}); err != nil {
		t.Fatal(err)
	}
	var board int64 = 1                // Work
	later := time.Now().Add(time.Hour) // "today" starts after the items were made

	// First run only records the day.
	if n, err := s.RollOver(ctx, board, "2026-10-12", later, true); err != nil || n != 0 {
		t.Fatalf("first run = %d, %v", n, err)
	}
	if n, _ := s.RollOver(ctx, board, "2026-10-13", later, true); n != 1 {
		t.Fatalf("next day carried %d, want 1 (only the open item)", n)
	}
	if n, _ := s.RollOver(ctx, board, "2026-10-13", later, true); n != 0 {
		t.Fatalf("same day again carried %d, want 0", n)
	}
	// A day that doesn't count (weekend on a work board) is recorded but not counted.
	if n, _ := s.RollOver(ctx, board, "2026-10-17", later, false); n != 0 {
		t.Fatalf("weekend carried %d", n)
	}
	it, _ := s.Item(ctx, items[0].ID)
	if it.CarriedDays != 1 {
		t.Fatalf("carried_days = %d, want 1", it.CarriedDays)
	}
	if fin, _ := s.Item(ctx, items[1].ID); fin.CarriedDays != 0 {
		t.Fatalf("done item carried %d", fin.CarriedDays)
	}
	// Items made today aren't "carried".
	if n, _ := s.RollOver(ctx, board, "2026-10-20", time.Now().Add(-time.Hour), true); n != 0 {
		t.Fatalf("items created today carried %d", n)
	}
}

func TestAcceptTimeboxes(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	if _, err := s.SetTimeboxes(ctx, 1, "2026-10-12", []NewTimebox{{Start: "07:00", End: "08:00", Kind: "task", Title: "Focus"}}); err != nil {
		t.Fatal(err)
	}
	out, err := s.AcceptTimeboxes(ctx, 1, "2026-10-12", true)
	if err != nil || len(out) != 1 || !out[0].Accepted {
		t.Fatalf("accept = %+v %v", out, err)
	}
	out, _ = s.AcceptTimeboxes(ctx, 1, "2026-10-12", false)
	if out[0].Accepted {
		t.Fatal("un-accept failed")
	}
}
