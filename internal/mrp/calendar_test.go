package mrp

import (
	"errors"
	"testing"
	"time"
)

// The calendar is deliberately free of database dependencies, so FR-5's hardest cases —
// the ones every draft of the algorithm got wrong — are table tests that run in
// microseconds with no Docker.

const (
	workStart = 9 * time.Hour
	workEnd   = 17 * time.Hour
)

// at builds a moment on the given day of August 2026 (the 3rd is a Monday).
func at(day, hour, min int) time.Time {
	return time.Date(2026, time.August, day, hour, min, 0, 0, time.UTC)
}

func testCalendar(t *testing.T, holidays ...int) *calendar {
	t.Helper()
	hs := map[time.Time]bool{}
	for _, d := range holidays {
		hs[at(d, 0, 0)] = true
	}
	// Mon 3 Aug .. Fri 21 Aug 2026, weekends excluded by buildCalendar.
	cal, err := buildCalendar(at(3, 0, 0), at(21, 0, 0), workStart, workEnd, hs)
	if err != nil {
		t.Fatalf("buildCalendar: %v", err)
	}
	return cal
}

func TestMinusWorkingDuration(t *testing.T) {
	tests := []struct {
		name     string
		holidays []int
		end      time.Time
		dur      time.Duration
		want     time.Time
	}{
		{
			// The case every version of the design failed. Clock arithmetic gives 08:00,
			// which is closed; snapping either way leaves only 2 working hours for a
			// 3-hour step. Consuming working time gives back the hour lost in the gap.
			name: "spills across the overnight gap",
			end:  at(12, 11, 0),
			dur:  3 * time.Hour,
			want: at(11, 16, 0),
		},
		{
			name: "fits inside one working day",
			end:  at(12, 16, 0),
			dur:  3 * time.Hour,
			want: at(12, 13, 0),
		},
		{
			name: "lands exactly on the start of a shift",
			end:  at(12, 17, 0),
			dur:  8 * time.Hour,
			want: at(12, 9, 0),
		},
		{
			name: "spans several whole days",
			end:  at(13, 12, 0),
			dur:  19 * time.Hour, // 3 on the 13th, 8 on the 12th, 8 on the 11th
			want: at(11, 9, 0),
		},
		{
			// FRD §10 acceptance scenario: a holiday must not absorb any duration.
			name:     "skips a holiday without spending time on it",
			holidays: []int{11},
			end:      at(12, 11, 0),
			dur:      3 * time.Hour,
			want:     at(10, 16, 0),
		},
		{
			// The reason a single `date -= 1` was never enough: Sat+Sun is the common case.
			name: "skips a weekend",
			end:  at(10, 11, 0), // Monday
			dur:  3 * time.Hour,
			want: at(7, 16, 0), // Friday
		},
		{
			name:     "skips a holiday adjacent to a weekend",
			holidays: []int{7}, // Friday
			end:      at(10, 11, 0),
			dur:      3 * time.Hour,
			want:     at(6, 16, 0), // Thursday
		},
		{
			// An end moment outside working hours snaps BACKWARD; it may never move later.
			name: "end after close snaps back to close",
			end:  at(12, 20, 0),
			dur:  2 * time.Hour,
			want: at(12, 15, 0),
		},
		{
			name: "end before open snaps back to the previous close",
			end:  at(12, 6, 0),
			dur:  1 * time.Hour,
			want: at(11, 16, 0),
		},
		{
			name: "zero duration returns the snapped end",
			end:  at(12, 20, 0),
			dur:  0,
			want: at(12, 17, 0),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cal := testCalendar(t, tc.holidays...)
			got, err := cal.minusWorkingDuration(tc.end, tc.dur)
			if err != nil {
				t.Fatalf("minusWorkingDuration: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("got %s, want %s", got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

// TestDurationIsConserved is the invariant, asserted directly rather than through examples:
// however many gaps the walk crosses, exactly `dur` working time must separate start and end.
func TestDurationIsConserved(t *testing.T) {
	cal := testCalendar(t, 11, 17)

	for _, dur := range []time.Duration{
		30 * time.Minute, 3 * time.Hour, 8 * time.Hour,
		9 * time.Hour, 25 * time.Hour, 40 * time.Hour,
	} {
		end := at(19, 14, 30)
		start, err := cal.minusWorkingDuration(end, dur)
		if err != nil {
			t.Fatalf("dur %s: %v", dur, err)
		}
		if got := cal.workingTimeBetween(start, end); got != dur {
			t.Errorf("dur %s: %s of working time between %s and %s",
				dur, got, start.Format(time.RFC3339), end.Format(time.RFC3339))
		}
	}
}

func TestSnapBackNeverMovesForward(t *testing.T) {
	cal := testCalendar(t, 11)
	for _, in := range []time.Time{
		at(12, 20, 0), at(12, 6, 0), at(11, 12, 0), // holiday
		at(9, 3, 0), at(12, 9, 0), at(12, 17, 0),
	} {
		got, err := cal.snapBack(in)
		if err != nil {
			t.Fatalf("snapBack(%s): %v", in, err)
		}
		if got.After(in) {
			t.Errorf("snapBack(%s) moved forward to %s", in.Format(time.RFC3339), got.Format(time.RFC3339))
		}
	}
}

func TestOutsideHorizonIsLoud(t *testing.T) {
	cal := testCalendar(t)

	// More work than the horizon holds must fail, not silently invent working days.
	if _, err := cal.minusWorkingDuration(at(4, 10, 0), 200*time.Hour); !errors.Is(err, ErrOutsideHorizon) {
		t.Errorf("want ErrOutsideHorizon, got %v", err)
	}
	if _, err := cal.snapBack(at(1, 10, 0)); !errors.Is(err, ErrOutsideHorizon) {
		t.Errorf("want ErrOutsideHorizon, got %v", err)
	}
}
