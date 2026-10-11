package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/paytonmurray51/workdesk/internal/db"
)

// RunJobs runs background work until ctx ends: today it's the midnight carry-over.
func (s *Server) RunJobs(ctx context.Context) {
	s.rollOver(ctx, time.Now())
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.rollOver(ctx, now)
		}
	}
}

// rollOver runs each board's carry-over once its local day changes. Unchecked items on
// daily lists simply stay on the list; this only counts the days they've been carried.
// Boards with a work schedule don't count Saturdays and Sundays.
func (s *Server) rollOver(ctx context.Context, now time.Time) {
	boards, err := s.store.AllBoards(ctx)
	if err != nil {
		slog.Error("carry-over: boards", "err", err)
		return
	}
	for _, b := range boards {
		loc, err := time.LoadLocation(b.Timezone)
		if err != nil {
			loc = time.UTC
		}
		local := now.In(loc)
		wd := local.Weekday()
		countDay := !b.HasSchedule || (wd != time.Saturday && wd != time.Sunday)
		n, err := s.store.RollOver(ctx, b.ID, local.Format("2006-01-02"), db.StartOfDay(now, b.Timezone), countDay)
		if err != nil {
			slog.Error("carry-over", "board", b.ID, "err", err)
			continue
		}
		if n > 0 {
			slog.Info("carry-over", "board", b.Name, "date", local.Format("2006-01-02"), "carried", n)
		}
	}
}
