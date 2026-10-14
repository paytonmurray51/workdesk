package db

import (
	"context"
	"errors"
	"testing"
)

func TestRemovedItemsStayRemoved(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	list := todayList(t, s, "Work")
	items, err := s.AddItems(ctx, list, []NewItem{{Title: "Reply to Sam", SourceID: "ABC-1"}, {Title: "manual"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if err := s.DeleteItem(ctx, it.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteItem(ctx, items[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}

	// The routine re-posts it: skipped. Another source_id still goes in.
	again, err := s.AddItems(ctx, list, []NewItem{{Title: "Reply to Sam", SourceID: "ABC-1"}, {Title: "New", SourceID: "ABC-2"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || *again[0].SourceID != "ABC-2" {
		t.Fatalf("re-post added %+v, want only ABC-2", again)
	}
	// Items without a source_id can always be added again.
	if xs, _ := s.AddItems(ctx, list, []NewItem{{Title: "manual"}}); len(xs) != 1 {
		t.Fatal("manual item not added")
	}
}
