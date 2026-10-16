// Package schedule computes civil-time repeats. It owns no timers or files.
package schedule

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const Layout = "2006-01-02T15:04"

type Definition struct {
	Message string  `json:"message"`
	Repeat  string  `json:"repeat"`
	At      string  `json:"at"`
	Cost    float64 `json:"cost"`
	Paused  bool    `json:"paused"`
}

func (d Definition) Validate() error {
	if strings.TrimSpace(d.Message) == "" || len(d.Message) > 1<<20 {
		return fmt.Errorf("a schedule needs a message of at most 1 MiB")
	}
	if math.IsNaN(d.Cost) || math.IsInf(d.Cost, 0) || d.Cost < 0 || d.Cost > 1e9 {
		return fmt.Errorf("the per-run spend limit must be between 0 and 1 billion dollars")
	}
	switch d.Repeat {
	case "once", "hourly", "daily", "weekdays", "weekly":
	default:
		return fmt.Errorf("repeat must be once, hourly, daily, weekdays or weekly")
	}
	if _, err := time.Parse(Layout, d.At); err != nil {
		return fmt.Errorf("time must be a local date and minute, YYYY-MM-DDTHH:MM")
	}
	return nil
}

// civil resolves a skipped minute to the next valid one and a repeated minute
// to its first occurrence. Using civil recurrence indices avoids firing twice
// in the hour a computer repeats when summer time ends.
func civil(t time.Time, loc *time.Location) time.Time {
	for {
		y, m, d := t.Date()
		h, min, _ := t.Clock()
		v := time.Date(y, m, d, h, min, 0, 0, loc)
		if v.Year() == y && v.Month() == m && v.Day() == d && v.Hour() == h && v.Minute() == min {
			// Offset changes need not be whole hours, as Lord Howe demonstrates.
			for n := 1; n <= 180; n++ {
				p := v.Add(-time.Duration(n) * time.Minute)
				if p.Year() == y && p.Month() == m && p.Day() == d && p.Hour() == h && p.Minute() == min {
					v = p
				}
			}
			return v
		}
		t = t.Add(time.Minute)
	}
}

func (d Definition) occurrence(n int, loc *time.Location) time.Time {
	at, _ := time.Parse(Layout, d.At)
	switch d.Repeat {
	case "hourly":
		at = at.AddDate(0, 0, n/24).Add(time.Duration(n%24) * time.Hour)
	case "daily":
		at = at.AddDate(0, 0, n)
	case "weekly":
		at = at.AddDate(0, 0, n*7)
	case "weekdays":
		for at.Weekday() == time.Saturday || at.Weekday() == time.Sunday {
			at = at.AddDate(0, 0, 1)
		}
		at = at.AddDate(0, 0, (n/5)*7)
		for left := n % 5; left > 0; {
			at = at.AddDate(0, 0, 1)
			if at.Weekday() != time.Saturday && at.Weekday() != time.Sunday {
				left--
			}
		}
	}
	return civil(at, loc)
}

func (d Definition) indexAfter(after time.Time, loc *time.Location) int {
	if d.Repeat == "once" {
		if d.occurrence(0, loc).After(after) {
			return 0
		}
		return 1
	}
	return sort.Search(1<<30, func(n int) bool { return d.occurrence(n, loc).After(after) })
}

func (d Definition) Next(after time.Time, loc *time.Location) time.Time {
	n := d.indexAfter(after, loc)
	if d.Repeat == "once" && n > 0 {
		return time.Time{}
	}
	return d.occurrence(n, loc)
}

// Due selects every occurrence after cursor through now, in one interval.
// Missed is how many additional times that one late run stands for.
func (d Definition) Due(cursor, now time.Time, loc *time.Location) (first, last time.Time, missed int) {
	start := d.indexAfter(cursor, loc)
	if d.Repeat == "once" && start > 0 {
		return
	}
	first = d.occurrence(start, loc)
	if first.After(now) {
		return time.Time{}, time.Time{}, 0
	}
	end := d.indexAfter(now, loc) - 1
	if d.Repeat == "once" {
		end = 0
	}
	last = d.occurrence(end, loc)
	missed = end - start
	return
}
