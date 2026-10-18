// Package schedule computes civil-time repeats. It owns no timers or files.
package schedule

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

const Layout = "2006-01-02T15:04"

// DateLayout is a rule's last day; ClockLayout a time of day.
const (
	DateLayout  = "2006-01-02"
	ClockLayout = "15:04"
)

// MinEvery is the shortest interval in minutes. A run is a model's turn, and
// one every few seconds is a cost nobody means to set.
const MinEvery = 5

// Definition is what a person chose: the message each run sends, when it
// runs, and the spend a run may reach. A schedule written before rules has
// Repeat and At, one of five repeats from a local minute; it is read as the
// rule it always meant (Timetable), so no log is rewritten.
type Definition struct {
	Message string  `json:"message"`
	Repeat  string  `json:"repeat,omitempty"`
	At      string  `json:"at,omitempty"`
	Rule    *Rule   `json:"rule,omitempty"`
	Cost    float64 `json:"cost"`
	Paused  bool    `json:"paused"`
}

// Rule is when a schedule runs, in the daemon's local civil time. Once is a
// single minute and nothing else applies. Otherwise the days are every day,
// the weekdays in Days, the Dates of each month, or the Nth weekday (Days'
// one) of each month, -1 being the last; the times of day are At, or one
// every Every minutes counted from Between's start (midnight without it)
// through its end. From is the first minute it may run at and Until its last
// day.
type Rule struct {
	Once    string   `json:"once,omitempty"`
	Every   int      `json:"every,omitempty"`
	Between []string `json:"between,omitempty"`
	At      []string `json:"at,omitempty"`
	Days    []string `json:"days,omitempty"`
	Dates   []int    `json:"dates,omitempty"`
	Nth     int      `json:"nth,omitempty"`
	From    string   `json:"from,omitempty"`
	Until   string   `json:"until,omitempty"`
}

// weekdays are indexed by time.Weekday; week is the order a person reads.
var (
	weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}
	week     = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
)

func (d Definition) Validate() error {
	if strings.TrimSpace(d.Message) == "" || len(d.Message) > 1<<20 {
		return fmt.Errorf("a schedule needs a message of at most 1 MiB")
	}
	if math.IsNaN(d.Cost) || math.IsInf(d.Cost, 0) || d.Cost < 0 || d.Cost > 1e9 {
		return fmt.Errorf("the per-run spend limit must be between 0 and 1 billion dollars")
	}
	if d.Rule != nil {
		if d.Repeat != "" || d.At != "" {
			return fmt.Errorf("a schedule has a rule or a repeat, not both")
		}
		return d.Rule.Validate()
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

func clock(s string) (int, error) {
	t, err := time.Parse(ClockLayout, s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a time of day, HH:MM", s)
	}
	return t.Hour()*60 + t.Minute(), nil
}

func (r Rule) Validate() error {
	if r.Once != "" {
		if r.Every != 0 || r.Nth != 0 || len(r.At)+len(r.Days)+len(r.Dates)+len(r.Between) > 0 || r.From != "" || r.Until != "" {
			return fmt.Errorf("a schedule that runs once has only its time")
		}
		if _, err := time.Parse(Layout, r.Once); err != nil {
			return fmt.Errorf("once must be a local date and minute, YYYY-MM-DDTHH:MM")
		}
		return nil
	}
	switch {
	case r.Every != 0 && len(r.At) > 0:
		return fmt.Errorf("a schedule runs at set times or every so often, not both")
	case r.Every == 0 && len(r.At) == 0:
		return fmt.Errorf("a schedule needs times of day, an interval, or a time to run once")
	case r.Every != 0 && (r.Every < MinEvery || r.Every > 24*60):
		return fmt.Errorf("the interval must be from %d minutes to 24 hours", MinEvery)
	case len(r.Between) > 0 && r.Every == 0:
		return fmt.Errorf("only an interval has hours to run between")
	case len(r.At) > 48:
		return fmt.Errorf("a schedule runs at most at 48 set times a day")
	}
	if len(r.Between) > 0 {
		if len(r.Between) != 2 {
			return fmt.Errorf("the hours to run between are a start and an end, HH:MM")
		}
		start, err := clock(r.Between[0])
		if err != nil {
			return err
		}
		end, err := clock(r.Between[1])
		if err != nil {
			return err
		}
		if start >= end {
			return fmt.Errorf("the hours to run between must start before they end, on the same day")
		}
	}
	for _, at := range r.At {
		if _, err := clock(at); err != nil {
			return err
		}
	}
	for _, day := range r.Days {
		if !slices.Contains(weekdays, day) {
			return fmt.Errorf("%q is not a weekday: mon, tue, wed, thu, fri, sat or sun", day)
		}
	}
	for _, date := range r.Dates {
		if date < 1 || date > 31 {
			return fmt.Errorf("a date of the month is from 1 to 31")
		}
	}
	switch {
	case r.Nth != 0 && (r.Nth < -1 || r.Nth > 5):
		return fmt.Errorf("the weekday of the month is the 1st to the 5th, or -1 for the last")
	case r.Nth != 0 && (len(r.Days) != 1 || len(r.Dates) > 0):
		return fmt.Errorf("a weekday of the month names one weekday and no dates")
	case len(r.Dates) > 0 && len(r.Days) > 0:
		return fmt.Errorf("a schedule runs on weekdays or on dates of the month, not both")
	}
	from, err := time.Parse(Layout, r.From)
	if err != nil {
		return fmt.Errorf("a repeating schedule needs the first minute it may run, YYYY-MM-DDTHH:MM")
	}
	if r.Until != "" {
		until, err := time.Parse(DateLayout, r.Until)
		if err != nil {
			return fmt.Errorf("the last day must be a date, YYYY-MM-DD")
		}
		if until.Before(from.Truncate(24 * time.Hour)) {
			return fmt.Errorf("the last day is before the first")
		}
	}
	return nil
}

// Normal is r written one way: days in the week's order, times and dates
// sorted, each once, clocks as HH:MM. Two rules that run at the same times
// are then equal, which is how a change that keeps the timing keeps the
// schedule's progress. Call it on a valid rule.
func (r Rule) Normal() Rule {
	n := r
	n.Days = nil
	for _, day := range week {
		if slices.Contains(r.Days, day) {
			n.Days = append(n.Days, day)
		}
	}
	n.At = nil
	for _, at := range r.At {
		m, _ := clock(at)
		n.At = append(n.At, fmt.Sprintf("%02d:%02d", m/60, m%60))
	}
	slices.Sort(n.At)
	n.At = slices.Compact(n.At)
	n.Between = nil
	for _, b := range r.Between {
		m, _ := clock(b)
		n.Between = append(n.Between, fmt.Sprintf("%02d:%02d", m/60, m%60))
	}
	n.Dates = slices.Compact(slices.Sorted(slices.Values(r.Dates)))
	return n
}

// Timetable is the definition's rule, a schedule from before rules read as
// the one its repeat meant: hourly is every hour at the first time's minute,
// weekly is the first time's weekday.
func (d Definition) Timetable() Rule {
	if d.Rule != nil {
		return d.Rule.Normal()
	}
	at, err := time.Parse(Layout, d.At)
	if err != nil {
		return Rule{}
	}
	from, times := at.Format(Layout), []string{at.Format(ClockLayout)}
	switch d.Repeat {
	case "once":
		return Rule{Once: from}
	case "hourly":
		return Rule{Every: 60, Between: []string{fmt.Sprintf("00:%02d", at.Minute()), fmt.Sprintf("23:%02d", at.Minute())}, From: from}
	case "daily":
		return Rule{At: times, From: from}
	case "weekdays":
		return Rule{At: times, Days: slices.Clone(week[:5]), From: from}
	case "weekly":
		return Rule{At: times, Days: []string{weekdays[at.Weekday()]}, From: from}
	}
	return Rule{}
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
			// A minute can be repeated only when the offset fell in the three
			// hours before it; looking only then keeps an interval of a few
			// minutes cheap to step through.
			_, offset := v.Zone()
			if _, before := v.Add(-180 * time.Minute).Zone(); before <= offset {
				return v
			}
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

// minutes are the rule's times of day, in minutes after midnight, in order.
func (r Rule) minutes() []int {
	var out []int
	if r.Every == 0 {
		for _, at := range r.At {
			if m, err := clock(at); err == nil {
				out = append(out, m)
			}
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	start, end := 0, 24*60-1
	if len(r.Between) == 2 {
		start, _ = clock(r.Between[0])
		end, _ = clock(r.Between[1])
	}
	for m := start; m <= end; m += r.Every {
		out = append(out, m)
	}
	return out
}

// on says whether the rule runs on a civil day.
func (r Rule) on(day time.Time) bool {
	y, m, d := day.Date()
	wd := weekdays[day.Weekday()]
	switch {
	case r.Nth > 0:
		return wd == r.Days[0] && (d-1)/7+1 == r.Nth
	case r.Nth < 0:
		return wd == r.Days[0] && d+7 > time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
	case len(r.Dates) > 0:
		return slices.Contains(r.Dates, d)
	case len(r.Days) > 0:
		return slices.Contains(r.Days, wd)
	}
	return true
}

// idleDays is how long each looks for a time before deciding the rule has no
// more: a valid rule's longest gap is a 5th weekday's, about three months.
const idleDays = 800

// each gives every time the rule runs after `after`, in order, until yield
// returns false or the rule has none left. Civil days and minutes are held as
// UTC times so that adding a day never meets a summer-time change.
func (r Rule) each(after time.Time, loc *time.Location, yield func(time.Time) bool) {
	if r.Once != "" {
		if at, err := time.Parse(Layout, r.Once); err == nil {
			if t := civil(at, loc); t.After(after) {
				yield(t)
			}
		}
		return
	}
	from, err := time.Parse(Layout, r.From)
	if err != nil {
		return
	}
	until, limited := time.Time{}, r.Until != ""
	if limited {
		if until, err = time.Parse(DateLayout, r.Until); err != nil {
			return
		}
	}
	day := from.Truncate(24 * time.Hour)
	if !after.IsZero() {
		a := after.In(loc)
		// The day before too: a minute skipped late in it can land after.
		if d := time.Date(a.Year(), a.Month(), a.Day()-1, 0, 0, 0, 0, time.UTC); d.After(day) {
			day = d
		}
	}
	minutes := r.minutes()
	last := after
	for idle := 0; idle < idleDays; day = day.AddDate(0, 0, 1) {
		if limited && day.After(until) {
			return
		}
		idle++
		if !r.on(day) {
			continue
		}
		for _, m := range minutes {
			c := day.Add(time.Duration(m) * time.Minute)
			if c.Before(from) {
				continue
			}
			t := civil(c, loc)
			if !t.After(last) {
				continue
			}
			last, idle = t, 0
			if !yield(t) {
				return
			}
		}
	}
}

// Next is the first time the schedule runs after `after`, or zero when it
// has none left.
func (d Definition) Next(after time.Time, loc *time.Location) time.Time {
	var next time.Time
	d.Timetable().each(after, loc, func(t time.Time) bool { next = t; return false })
	return next
}

// Due selects every occurrence after cursor through now, in one interval.
// Missed is how many additional times that one late run stands for.
func (d Definition) Due(cursor, now time.Time, loc *time.Location) (first, last time.Time, missed int) {
	n := 0
	d.Timetable().each(cursor, loc, func(t time.Time) bool {
		if t.After(now) {
			return false
		}
		if n == 0 {
			first = t
		}
		last, n = t, n+1
		return true
	})
	if n == 0 {
		return time.Time{}, time.Time{}, 0
	}
	return first, last, n - 1
}

// Label is a time as the console and the TUI print it: the weekday, the
// date, the year when it is not now's, and the minute, in the time's own
// zone.
func Label(t, now time.Time) string {
	return day(t, now) + " " + t.Format(ClockLayout)
}

func day(t, now time.Time) string {
	if t.Year() != now.Year() {
		return t.Format("Mon 2 Jan 2006")
	}
	return t.Format("Mon 2 Jan")
}

// Sentence says when the schedule runs, in words: "Every 30 minutes from
// 09:00 to 18:00 on weekdays", "Tuesdays and Thursdays at 15:00". The first
// day is said only when it is after now's.
func (d Definition) Sentence(now time.Time) string {
	r := d.Timetable()
	if r.Once != "" {
		at, err := time.Parse(Layout, r.Once)
		if err != nil {
			return ""
		}
		return "Once, " + day(at, now) + " at " + at.Format(ClockLayout)
	}
	var b strings.Builder
	days := r.dayWords()
	if r.Every > 0 {
		b.WriteString("Every " + interval(r.Every))
		if len(r.Between) == 2 {
			b.WriteString(" from " + r.Between[0] + " to " + r.Between[1])
		}
		if days != "" {
			b.WriteString(" on " + days)
		}
	} else {
		switch {
		case days == "":
			b.WriteString("Every day")
		case len(r.Dates) > 0 || r.Nth != 0:
			b.WriteString("On " + days)
		default:
			b.WriteString(strings.ToUpper(days[:1]) + days[1:])
		}
		b.WriteString(" at " + list(r.At))
	}
	if from, err := time.Parse(Layout, r.From); err == nil {
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		if from.Truncate(24 * time.Hour).After(today) {
			b.WriteString(", starting " + day(from, now))
		}
	}
	if until, err := time.Parse(DateLayout, r.Until); err == nil {
		b.WriteString(", until " + day(until, now))
	}
	return b.String()
}

func interval(minutes int) string {
	switch {
	case minutes == 60:
		return "hour"
	case minutes%60 == 0:
		return fmt.Sprintf("%d hours", minutes/60)
	}
	return fmt.Sprintf("%d minutes", minutes)
}

var (
	dayNames = map[string]string{"mon": "Mondays", "tue": "Tuesdays", "wed": "Wednesdays", "thu": "Thursdays", "fri": "Fridays", "sat": "Saturdays", "sun": "Sundays"}
	ordinals = []string{"", "first", "second", "third", "fourth", "fifth"}
)

// dayWords names the days a normal rule runs on, or "" for every day.
func (r Rule) dayWords() string {
	switch {
	case r.Nth != 0:
		nth := "last"
		if r.Nth > 0 {
			nth = ordinals[r.Nth]
		}
		return "the " + nth + " " + strings.TrimSuffix(dayNames[r.Days[0]], "s") + " of each month"
	case len(r.Dates) > 0:
		var dates []string
		for _, d := range r.Dates {
			dates = append(dates, ordinal(d))
		}
		return "the " + list(dates) + " of each month"
	case len(r.Days) == 0 || len(r.Days) == 7:
		return ""
	case slices.Equal(r.Days, week[:5]):
		return "weekdays"
	case slices.Equal(r.Days, []string{"sat", "sun"}):
		return "weekends"
	}
	var names []string
	for _, d := range r.Days {
		names = append(names, dayNames[d])
	}
	return list(names)
}

func ordinal(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 13:
		return fmt.Sprintf("%dth", n)
	case n%10 == 1:
		return fmt.Sprintf("%dst", n)
	case n%10 == 2:
		return fmt.Sprintf("%dnd", n)
	case n%10 == 3:
		return fmt.Sprintf("%drd", n)
	}
	return fmt.Sprintf("%dth", n)
}

func list(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
