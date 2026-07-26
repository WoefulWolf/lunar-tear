package timeline

import (
	"testing"
	"time"
)

func ms(t time.Time) int64 { return t.UTC().UnixMilli() }

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Global mode must be a no-op: the content clock is real time, so every
// availability check behaves exactly as it did before per-player timelines.
func TestGlobalModeIsANoOp(t *testing.T) {
	r, err := NewResolver(ModeGlobal, ms(day(2026, time.July, 21)), 90)
	if err != nil {
		t.Fatal(err)
	}

	for _, reg := range []int64{0, ms(day(2020, time.January, 1)), ms(day(2026, time.July, 24))} {
		if got := r.ShiftFor(reg); got != 0 {
			t.Errorf("ShiftFor(%d) = %d, want 0", reg, got)
		}
		if got := r.Cohort(reg); got != 0 {
			t.Errorf("Cohort(%d) = %d, want 0", reg, got)
		}
	}
	now := ms(day(2026, time.July, 26))
	if got := r.At(now, ms(day(2020, time.January, 1))); int64(got) != now {
		t.Errorf("At = %d, want real now %d", got, now)
	}
	if got := r.CohortCount(); got != 1 {
		t.Errorf("CohortCount = %d, want 1", got)
	}
}

func TestShiftForPerPlayer(t *testing.T) {
	anchor := ms(day(2026, time.July, 21))
	r, err := NewResolver(ModePerPlayer, anchor, 90)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		register time.Time
		wantDays int64
	}{
		{"registered on the anchor day", day(2026, time.July, 21), 0},
		{"one day after", day(2026, time.July, 22), 1},
		{"five days after", day(2026, time.July, 26), 5},
		{"a full loop later wraps to zero", day(2026, time.October, 19), 0},
		{"one day before the anchor wraps to the end", day(2026, time.July, 20), 89},
		// 2393 days before the anchor; -2393 mod 90 = 37.
		{"long before the anchor still lands in range", day(2020, time.January, 1), 37},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.ShiftFor(ms(tt.register))
			if want := tt.wantDays * msDay; got != want {
				t.Errorf("ShiftFor = %d (%d days), want %d (%d days)",
					got, got/msDay, want, tt.wantDays)
			}
		})
	}
}

// Registration time of day must not matter — everyone registering on the same
// day shares a cohort.
func TestRegistrationTimeOfDayIsIgnored(t *testing.T) {
	anchor := ms(day(2026, time.July, 21))
	r, _ := NewResolver(ModePerPlayer, anchor, 90)

	base := day(2026, time.July, 24)
	want := r.ShiftFor(ms(base))
	for _, offset := range []time.Duration{time.Second, 6 * time.Hour, 23*time.Hour + 59*time.Minute} {
		if got := r.ShiftFor(ms(base.Add(offset))); got != want {
			t.Errorf("registering %v into the day gave shift %d, want %d", offset, got, want)
		}
	}
}

// The shift must always be a whole number of days so day boundaries stay lined
// up between the content clock and real time (the daily reset depends on it).
func TestShiftIsAlwaysWholeDays(t *testing.T) {
	// Deliberately not midnight-aligned.
	anchor := ms(day(2026, time.July, 21).Add(7*time.Hour + 13*time.Minute))
	r, _ := NewResolver(ModePerPlayer, anchor, 90)

	base := day(2026, time.March, 2)
	for i := 0; i < 200; i++ {
		shift := r.ShiftFor(ms(base.AddDate(0, 0, i)))
		if shift%msDay != 0 {
			t.Fatalf("registering on day %d gave a shift of %d ms, not a whole number of days", i, shift)
		}
		if shift < 0 || shift >= r.loopMillis {
			t.Fatalf("shift %d outside [0, loop)", shift)
		}
	}
}

func TestCohort(t *testing.T) {
	anchor := ms(day(2026, time.July, 21))
	r, _ := NewResolver(ModePerPlayer, anchor, 90)

	if got, want := r.CohortCount(), 90; got != want {
		t.Errorf("CohortCount = %d, want %d", got, want)
	}
	for i := 0; i < 90; i++ {
		reg := ms(day(2026, time.July, 21).AddDate(0, 0, i))
		if got := r.Cohort(reg); got != i {
			t.Errorf("registering %d days after the anchor gave cohort %d", i, got)
		}
	}
	// Cohorts are bounded by the loop, not by the calendar.
	if got := r.Cohort(ms(day(2026, time.July, 21).AddDate(0, 0, 90))); got != 0 {
		t.Errorf("a full loop later gave cohort %d, want 0", got)
	}
}

// A player's content clock at time T must equal the global clock at T - shift.
// This is what lets the server keep one archive while each player sees a
// different phase.
func TestAtRunsBehindByTheShift(t *testing.T) {
	anchor := ms(day(2026, time.July, 21))
	r, _ := NewResolver(ModePerPlayer, anchor, 90)

	now := ms(day(2026, time.August, 1).Add(9 * time.Hour))
	reg := ms(day(2026, time.July, 26)) // 5 days after the anchor

	got := r.At(now, reg)
	if want := ContentMillis(now - 5*msDay); got != want {
		t.Errorf("At = %d, want %d", got, want)
	}
}

func TestNewResolverRejectsBadLoop(t *testing.T) {
	if _, err := NewResolver(ModePerPlayer, 0, 0); err == nil {
		t.Error("per-player mode with a zero loop should be rejected")
	}
	if _, err := NewResolver(ModeGlobal, 0, 0); err != nil {
		t.Errorf("global mode should not care about the loop length: %v", err)
	}
}

func TestFromEnv(t *testing.T) {
	t.Run("unset is global", func(t *testing.T) {
		r, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if r.Mode() != ModeGlobal {
			t.Errorf("mode = %v, want global", r.Mode())
		}
	})

	t.Run("per-player", func(t *testing.T) {
		t.Setenv("LUNAR_TIMELINE_MODE", "per-player")
		t.Setenv("LUNAR_TIMELINE_ANCHOR", "2026-07-21T00:00:00Z")
		t.Setenv("LUNAR_TIMELINE_LOOP_DAYS", "90")

		r, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if r.Mode() != ModePerPlayer {
			t.Fatalf("mode = %v, want per-player", r.Mode())
		}
		if got, want := r.ShiftFor(ms(day(2026, time.July, 24))), int64(3*msDay); got != want {
			t.Errorf("ShiftFor = %d, want %d", got, want)
		}
	})

	t.Run("per-player without a loop length is an error", func(t *testing.T) {
		t.Setenv("LUNAR_TIMELINE_MODE", "per-player")
		if _, err := FromEnv(); err == nil {
			t.Error("expected an error")
		}
	})

	t.Run("bad mode is an error", func(t *testing.T) {
		t.Setenv("LUNAR_TIMELINE_MODE", "yesplease")
		if _, err := FromEnv(); err == nil {
			t.Error("expected an error")
		}
	})

	t.Run("bad anchor is an error", func(t *testing.T) {
		t.Setenv("LUNAR_TIMELINE_ANCHOR", "last tuesday")
		if _, err := FromEnv(); err == nil {
			t.Error("expected an error")
		}
	})
}
