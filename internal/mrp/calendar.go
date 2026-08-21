package mrp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// ErrOutsideHorizon is returned when backward scheduling walks off the front of the
// precomputed calendar. It is deliberately loud: silently treating unknown days as working
// days would produce a plausible-looking schedule that is simply wrong.
var ErrOutsideHorizon = errors.New("scheduling walked outside the calendar horizon")

// interval is one contiguous block of working time, [start, end).
type interval struct {
	start time.Time
	end   time.Time
}

// calendar is a plant's working time over a bounded horizon, held as non-overlapping
// intervals in ascending order. Holidays and weekends are absent rather than marked —
// a non-working day contributes no interval at all, which is what lets the walk skip it
// without spending any duration on it.
//
// All three questions FR-5 asks — is this a holiday, is this inside business hours, how
// much working time lies between two moments — reduce to lookups over this one structure.
type calendar struct {
	intervals []interval
}

// locate returns the index of the last interval that starts at or before t.
func (c *calendar) locate(t time.Time) (int, error) {
	i := sort.Search(len(c.intervals), func(i int) bool {
		return c.intervals[i].start.After(t)
	}) - 1
	if i < 0 {
		return 0, fmt.Errorf("%w: %s precedes the horizon", ErrOutsideHorizon, t.Format(time.RFC3339))
	}
	return i, nil
}

// snapBack returns t if it is already a working moment, otherwise the end of the latest
// working interval strictly before it.
//
// It only ever moves backward, and it is only ever applied to END moments. A due moment
// that moved later would finish after the step that depends on it has already started.
func (c *calendar) snapBack(t time.Time) (time.Time, error) {
	interval, err := c.locate(t)
	if err != nil {
		return time.Time{}, err
	}
	if t.Before(c.intervals[interval].end) {
		return t, nil
	}

	return c.intervals[interval].end, nil
}

// minusWorkingDuration returns the moment exactly d of WORKING time before end.
// if this must be finished by X, and it takes N hours of work, when do I start?
func (c *calendar) minusWorkingDuration(end time.Time, d time.Duration) (time.Time, error) {
	if d < 0 {
		return time.Time{}, fmt.Errorf("negative duration %s", d)
	}

	m, err := c.snapBack(end)
	if err != nil {
		return time.Time{}, err
	}
	i, err := c.locate(m)

	if err != nil {
		return time.Time{}, err
	}

	remaining := d
	for {
		// Working time inside this interval, before m. On the first pass m is the deadline
		// itself so this is a partial day; afterwards m is an interval's close, so a full one.
		available := m.Sub(c.intervals[i].start)
		if available >= remaining {
			return m.Add(-remaining), nil
		}
		remaining -= available

		// Go back to previous interval. The closed gap in between consumes nothing, which
		// is what makes the duration come out exact.
		i--
		if i < 0 {
			return time.Time{}, fmt.Errorf(
				"%w: need %s more working time before %s",
				ErrOutsideHorizon, remaining, end.Format(time.RFC3339))
		}
		m = c.intervals[i].end
	}
}

// workingTimeBetween returns how much working time lies in [from, to]. It is the inverse of
// minusWorkingDuration and exists so the duration invariant can be asserted directly rather
// than inferred from example dates.
func (c *calendar) workingTimeBetween(from, to time.Time) time.Duration {
	var total time.Duration
	for _, iv := range c.intervals {
		start, end := iv.start, iv.end
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		if end.After(start) {
			total += end.Sub(start)
		}
	}
	return total
}

// buildCalendar materialises one working interval per working day in [from, to].
//
// Note the schema has no weekend concept: holidays(plant_id, holiday) holds explicit dates
// only. Weekends are therefore applied here rather than seeded as rows, so that the common
// "two consecutive non-working days" case works without depending on seed data.
func buildCalendar(from, to time.Time, workStart, workEnd time.Duration, holidays map[time.Time]bool) (*calendar, error) {
	if workEnd <= workStart {
		return nil, fmt.Errorf("plant work_end %s is not after work_start %s", workEnd, workStart)
	}

	var intervals []interval
	for day := truncateDay(from); !day.After(truncateDay(to)); day = day.AddDate(0, 0, 1) {
		switch day.Weekday() {
		case time.Saturday, time.Sunday:
			continue
		}
		if holidays[day] {
			continue
		}
		intervals = append(intervals, interval{start: day.Add(workStart), end: day.Add(workEnd)})
	}
	if len(intervals) == 0 {
		return nil, fmt.Errorf("%w: no working days in %s..%s",
			ErrOutsideHorizon, from.Format(time.DateOnly), to.Format(time.DateOnly))
	}
	return &calendar{intervals: intervals}, nil
}

func truncateDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

const plantHoursQuery = `
SELECT
	EXTRACT(EPOCH FROM p.work_start)::bigint,
	EXTRACT(EPOCH FROM p.work_end)::bigint
FROM production_plans pl
JOIN warehouses w ON w.id = pl.warehouse_id
JOIN plants     p ON p.id = w.plant_id
WHERE pl.id = $1
`

const holidaysQuery = `
SELECT h.holiday
FROM production_plans pl
JOIN warehouses w ON w.id = pl.warehouse_id
JOIN holidays   h ON h.plant_id = w.plant_id
WHERE pl.id = $1 AND h.holiday BETWEEN $2 AND $3
`

// loadCalendar precomputes the plan's plant calendar in two queries, once per run.
//
// The horizon cannot be derived — how far back the walk reaches is only known once it has
// finished — so it is chosen: enough working days to absorb the whole tree's duration,
// doubled to leave room for non-working days, plus a fixed margin. Running off the end
// raises ErrOutsideHorizon rather than guessing.
func loadCalendar(ctx context.Context, q querier, planID int64, until time.Time, totalWork time.Duration) (*calendar, error) {
	var startSecs, endSecs int64
	if err := q.QueryRow(ctx, plantHoursQuery, planID).Scan(&startSecs, &endSecs); err != nil {
		return nil, fmt.Errorf("load plant hours: %w", err)
	}
	workStart := time.Duration(startSecs) * time.Second
	workEnd := time.Duration(endSecs) * time.Second

	dayLength := workEnd - workStart
	if dayLength <= 0 {
		return nil, fmt.Errorf("plant work_end %s is not after work_start %s", workEnd, workStart)
	}
	workingDays := int(math.Ceil(float64(totalWork) / float64(dayLength)))
	from := until.AddDate(0, 0, -(workingDays*2 + 30))

	rows, err := q.Query(ctx, holidaysQuery, planID, from, until)
	if err != nil {
		return nil, fmt.Errorf("load holidays: %w", err)
	}
	defer rows.Close()

	holidays := map[time.Time]bool{}
	for rows.Next() {
		var h time.Time
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		holidays[truncateDay(h)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return buildCalendar(from, until, workStart, workEnd, holidays)
}
