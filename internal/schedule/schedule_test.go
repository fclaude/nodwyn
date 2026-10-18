package schedule

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

func TestCivilRepeatsAndMissedTimes(t *testing.T) {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ repeat, at, after, want string }{
		{"daily", "2026-03-07T02:30", "2026-03-07T10:30:00Z", "2026-03-08T10:00:00Z"},
		{"hourly", "2026-11-01T00:30", "2026-11-01T08:30:00Z", "2026-11-01T10:30:00Z"},
		{"weekdays", "2026-10-16T09:00", "2026-10-16T16:00:00Z", "2026-10-19T16:00:00Z"},
		{"weekdays", "2026-10-17T09:00", "2026-10-15T00:00:00Z", "2026-10-19T16:00:00Z"},
		{"weekly", "2026-03-01T09:00", "2026-03-01T17:00:00Z", "2026-03-08T16:00:00Z"},
		{"once", "2026-11-01T01:30", "2026-11-01T09:00:00Z", ""},
	} {
		t.Run(tc.repeat+tc.at, func(t *testing.T) {
			d := Definition{Message: "check", Repeat: tc.repeat, At: tc.at}
			if err := d.Validate(); err != nil {
				t.Fatal(err)
			}
			after, err := time.Parse(time.RFC3339, tc.after)
			if err != nil {
				t.Fatal(err)
			}
			got := d.Next(after, loc)
			want := time.Time{}
			if tc.want != "" {
				want, err = time.Parse(time.RFC3339, tc.want)
				if err != nil {
					t.Fatal(err)
				}
			}
			if !got.Equal(want) {
				t.Fatalf("got %s want %s", got, want)
			}
		})
	}
	d := Definition{Message: "check", Repeat: "daily", At: "2026-10-10T09:00"}
	now := time.Date(2026, 10, 15, 17, 0, 0, 0, time.UTC)
	first, last, missed := d.Due(time.Time{}, now, loc)
	if first.Day() != 10 || last.Day() != 15 || missed != 5 {
		t.Fatalf("%v %v %d", first, last, missed)
	}
	if next := d.Next(last, loc); next.Day() != 16 {
		t.Fatal(next)
	}
	if f, _, _ := d.Due(last, now, loc); !f.IsZero() {
		t.Fatal("consumed time fired again")
	}
}

func TestHalfHourSummerTime(t *testing.T) {
	loc, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Fatal(err)
	}
	d := Definition{Message: "check", Repeat: "once", At: "2026-10-04T02:15"}
	got := d.Next(time.Time{}, loc)
	if got.Hour() != 2 || got.Minute() != 30 {
		t.Fatal(got)
	}
	d.At = "2026-04-05T01:45"
	got = d.Next(time.Time{}, loc)
	_, offset := got.Zone()
	if offset != 11*3600 {
		t.Fatal(got)
	}
}

func TestInvalidDefinitions(t *testing.T) {
	for _, d := range []Definition{{}, {Message: "x", Repeat: "cron", At: "2026-10-15T10:00"}, {Message: "x", Repeat: "daily", At: "2026-02-30T10:00"}, {Message: "x", Repeat: "once", At: "2026-10-15T10:00", Cost: -1}} {
		if d.Validate() == nil {
			t.Fatalf("accepted %+v", d)
		}
	}
}

// legacyCivil and legacyOccurrence are the five repeats' arithmetic as it was
// before rules (ADR 49 §4), kept as the oracle that a schedule from before
// rules still runs at the same times now that it is read as a rule.
func legacyCivil(t time.Time, loc *time.Location) time.Time {
	for {
		y, m, d := t.Date()
		h, min, _ := t.Clock()
		v := time.Date(y, m, d, h, min, 0, 0, loc)
		if v.Year() == y && v.Month() == m && v.Day() == d && v.Hour() == h && v.Minute() == min {
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

func legacyOccurrence(d Definition, n int, loc *time.Location) time.Time {
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
	return legacyCivil(at, loc)
}

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestRepeatsFromBeforeRulesKeepTheirTimes(t *testing.T) {
	for _, tc := range []struct{ zone, repeat, at string }{
		{"America/Los_Angeles", "hourly", "2026-10-30T00:30"},
		{"America/Los_Angeles", "hourly", "2027-03-12T22:00"},
		{"America/Los_Angeles", "daily", "2026-03-06T02:30"},
		{"America/Los_Angeles", "weekdays", "2026-10-17T09:00"},
		{"America/Los_Angeles", "weekly", "2026-03-01T01:30"},
		{"America/Los_Angeles", "once", "2026-11-01T01:30"},
		{"Australia/Lord_Howe", "hourly", "2026-10-02T23:15"},
		{"Australia/Lord_Howe", "daily", "2026-04-03T01:45"},
		{"America/Santiago", "hourly", "2026-09-05T20:30"},
		{"America/Santiago", "daily", "2026-09-04T00:15"},
		{"America/Santiago", "daily", "2026-04-02T23:30"},
		{"Europe/London", "weekdays", "2026-03-27T01:30"},
		{"Asia/Kolkata", "weekly", "2026-01-05T08:00"},
	} {
		t.Run(tc.zone+" "+tc.repeat+" "+tc.at, func(t *testing.T) {
			loc := zone(t, tc.zone)
			d := Definition{Message: "check", Repeat: tc.repeat, At: tc.at}
			if err := d.Validate(); err != nil {
				t.Fatal(err)
			}
			if err := d.Timetable().Validate(); err != nil {
				t.Fatalf("the rule %+v it is read as: %v", d.Timetable(), err)
			}
			count := 400
			if tc.repeat == "once" {
				count = 1
			}
			// The old arithmetic gave a skipped hour's minute twice (02:00 became
			// 03:00, then 03:00), and its Next, a search for the first time
			// after, gave it once: so does the oracle.
			after := time.Time{}
			for n := 0; n < count; n++ {
				want := legacyOccurrence(d, n, loc)
				if !want.After(after) {
					continue
				}
				if got := d.Next(after, loc); !got.Equal(want) {
					t.Fatalf("time %d: got %s, before rules %s", n, got, want)
				}
				after = want
			}
			if tc.repeat == "once" && !d.Next(after, loc).IsZero() {
				t.Fatal("a schedule that runs once ran again")
			}
		})
	}
}

// local writes times as the daemon's local minutes, which is how a person
// reads a timetable.
func local(times []time.Time, loc *time.Location) []string {
	var out []string
	for _, t := range times {
		out = append(out, t.In(loc).Format(Layout))
	}
	return out
}

func nextTimes(d Definition, after time.Time, loc *time.Location, n int) []time.Time {
	var out []time.Time
	for len(out) < n {
		next := d.Next(after, loc)
		if next.IsZero() {
			break
		}
		out = append(out, next)
		after = next
	}
	return out
}

func TestRulesRunWhenTheySay(t *testing.T) {
	loc := zone(t, "America/Los_Angeles")
	for _, tc := range []struct {
		name  string
		rule  Rule
		after string
		want  []string
	}{
		{"every 30 minutes, counted from midnight", Rule{Every: 30, From: "2026-10-19T08:10"}, "", []string{"2026-10-19T08:30", "2026-10-19T09:00", "2026-10-19T09:30"}},
		{"every 30 minutes in working hours on weekdays", Rule{Every: 30, Between: []string{"09:00", "18:00"}, Days: []string{"mon", "tue", "wed", "thu", "fri"}, From: "2026-10-19T00:00"}, "2026-10-23T17:15", []string{"2026-10-23T17:30", "2026-10-23T18:00", "2026-10-26T09:00", "2026-10-26T09:30"}},
		{"every 45 minutes from 09:00 starts again each day", Rule{Every: 45, Between: []string{"09:00", "11:00"}, From: "2026-10-19T00:00"}, "", []string{"2026-10-19T09:00", "2026-10-19T09:45", "2026-10-19T10:30", "2026-10-20T09:00"}},
		{"Tuesdays and Thursdays at 15:00", Rule{At: []string{"15:00"}, Days: []string{"thu", "tue"}, From: "2026-10-16T12:00"}, "", []string{"2026-10-20T15:00", "2026-10-22T15:00", "2026-10-27T15:00"}},
		{"every day at 9 and half past 5", Rule{At: []string{"17:30", "9:00"}, From: "2026-10-19T10:00"}, "", []string{"2026-10-19T17:30", "2026-10-20T09:00", "2026-10-20T17:30"}},
		{"the first Monday of each month", Rule{At: []string{"10:00"}, Days: []string{"mon"}, Nth: 1, From: "2026-10-17T00:00"}, "", []string{"2026-11-02T10:00", "2026-12-07T10:00", "2027-01-04T10:00"}},
		{"the last Friday of each month", Rule{At: []string{"17:00"}, Days: []string{"fri"}, Nth: -1, From: "2026-10-01T00:00"}, "", []string{"2026-10-30T17:00", "2026-11-27T17:00", "2026-12-25T17:00"}},
		{"the 1st and 15th", Rule{At: []string{"08:00"}, Dates: []int{15, 1}, From: "2026-10-02T00:00"}, "", []string{"2026-10-15T08:00", "2026-11-01T08:00", "2026-11-15T08:00"}},
		{"the 31st skips the months without one", Rule{At: []string{"08:00"}, Dates: []int{31}, From: "2026-10-02T00:00"}, "", []string{"2026-10-31T08:00", "2026-12-31T08:00", "2027-01-31T08:00"}},
		{"until its last day", Rule{At: []string{"09:00"}, From: "2026-10-18T00:00", Until: "2026-10-20"}, "", []string{"2026-10-18T09:00", "2026-10-19T09:00", "2026-10-20T09:00"}},
		{"nothing before the first minute", Rule{At: []string{"09:00"}, From: "2026-10-20T09:01"}, "2026-10-17T00:00", []string{"2026-10-21T09:00"}},
		{"once", Rule{Once: "2026-10-20T14:00"}, "", []string{"2026-10-20T14:00"}},
		{"every 30 minutes across spring forward", Rule{Every: 30, From: "2027-03-14T00:00"}, "2027-03-14T01:00", []string{"2027-03-14T01:30", "2027-03-14T03:00", "2027-03-14T03:30"}},
		{"every 30 minutes across fall back", Rule{Every: 30, From: "2026-11-01T00:00"}, "2026-11-01T00:00", []string{"2026-11-01T00:30", "2026-11-01T01:00", "2026-11-01T01:30", "2026-11-01T02:00"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := tc.rule
			d := Definition{Message: "check", Rule: &rule}
			if err := d.Validate(); err != nil {
				t.Fatal(err)
			}
			after := time.Time{}
			if tc.after != "" {
				civilAfter, err := time.ParseInLocation(Layout, tc.after, loc)
				if err != nil {
					t.Fatal(err)
				}
				after = civilAfter
			}
			got := local(nextTimes(d, after, loc, len(tc.want)+1), loc)
			if tc.name == "until its last day" || tc.name == "once" {
				if len(got) != len(tc.want) {
					t.Fatalf("ran past its end: %v", got)
				}
			}
			if len(got) > len(tc.want) {
				got = got[:len(tc.want)]
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	// The hour that is repeated in autumn runs once, in its first occurrence.
	d := Definition{Message: "check", Rule: &Rule{Every: 30, From: "2026-11-01T00:00"}}
	times := nextTimes(d, time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC), loc, 4)
	for i, offset := range []int{-7, -7, -7, -8} {
		if _, got := times[i].Zone(); got != offset*3600 {
			t.Fatalf("time %d %s: offset %d", i, times[i], got)
		}
	}
}

func TestARuleCoalescesMissedTimes(t *testing.T) {
	loc := zone(t, "America/Los_Angeles")
	d := Definition{Message: "check", Rule: &Rule{Every: 30, From: "2026-10-19T00:00"}}
	cursor := time.Date(2026, 10, 19, 9, 0, 0, 0, loc)
	first, last, missed := d.Due(cursor, time.Date(2026, 10, 19, 11, 10, 0, 0, loc), loc)
	if first.Format(Layout) != "2026-10-19T09:30" || last.Format(Layout) != "2026-10-19T11:00" || missed != 3 {
		t.Fatalf("%s %s %d", first, last, missed)
	}
	if f, _, _ := d.Due(last, time.Date(2026, 10, 19, 11, 10, 0, 0, loc), loc); !f.IsZero() {
		t.Fatal("a consumed time was due again")
	}
	// A week asleep is one late run standing for every time it missed.
	_, _, missed = d.Due(cursor, cursor.AddDate(0, 0, 7), loc)
	if missed != 7*48-1 {
		t.Fatal(missed)
	}
}

func TestInvalidRules(t *testing.T) {
	from := "2026-10-19T00:00"
	for _, r := range []Rule{
		{},
		{Every: 4, From: from},
		{Every: 24*60 + 1, From: from},
		{Every: 30, At: []string{"09:00"}, From: from},
		{Every: 30, Between: []string{"18:00", "09:00"}, From: from},
		{Every: 30, Between: []string{"09:00"}, From: from},
		{At: []string{"09:00"}, Between: []string{"09:00", "18:00"}, From: from},
		{At: []string{"25:00"}, From: from},
		{At: []string{"09:00"}, Days: []string{"monday"}, From: from},
		{At: []string{"09:00"}, Dates: []int{0}, From: from},
		{At: []string{"09:00"}, Dates: []int{32}, From: from},
		{At: []string{"09:00"}, Dates: []int{1}, Days: []string{"mon"}, From: from},
		{At: []string{"09:00"}, Nth: 1, From: from},
		{At: []string{"09:00"}, Nth: 6, Days: []string{"mon"}, From: from},
		{At: []string{"09:00"}, Nth: 1, Days: []string{"mon", "tue"}, From: from},
		{At: []string{"09:00"}},
		{At: []string{"09:00"}, From: from, Until: "2026-10-18"},
		{At: []string{"09:00"}, From: from, Until: "next week"},
		{Once: "2026-10-20T14:00", Days: []string{"mon"}},
		{Once: "tomorrow"},
	} {
		rule := r
		if (Definition{Message: "x", Rule: &rule}).Validate() == nil {
			t.Errorf("accepted %+v", r)
		}
	}
	if (Definition{Message: "x", Repeat: "daily", At: from, Rule: &Rule{Once: from}}).Validate() == nil {
		t.Error("accepted a rule and a repeat together")
	}
}

func TestSentencesSayTheRule(t *testing.T) {
	now := time.Date(2026, 10, 17, 16, 51, 0, 0, time.UTC)
	for _, tc := range []struct {
		rule Rule
		want string
	}{
		{Rule{Every: 30, From: "2026-10-17T16:51"}, "Every 30 minutes"},
		{Rule{Every: 30, Between: []string{"9:00", "18:00"}, Days: []string{"fri", "mon", "tue", "wed", "thu"}, From: "2026-10-17T16:51"}, "Every 30 minutes from 09:00 to 18:00 on weekdays"},
		{Rule{Every: 120, Days: []string{"sat", "sun"}, From: "2026-10-17T16:51"}, "Every 2 hours on weekends"},
		{Rule{Every: 60, Dates: []int{1}, From: "2026-10-17T16:51"}, "Every hour on the 1st of each month"},
		{Rule{At: []string{"15:00"}, Days: []string{"tue", "thu"}, From: "2026-10-17T16:51"}, "Tuesdays and Thursdays at 15:00"},
		{Rule{At: []string{"09:00", "17:30"}, From: "2026-10-17T16:51"}, "Every day at 09:00 and 17:30"},
		{Rule{At: []string{"09:00"}, Days: []string{"mon", "wed", "fri"}, From: "2026-10-17T16:51"}, "Mondays, Wednesdays and Fridays at 09:00"},
		{Rule{At: []string{"10:00"}, Days: []string{"mon"}, Nth: 1, From: "2026-10-17T16:51"}, "On the first Monday of each month at 10:00"},
		{Rule{At: []string{"17:00"}, Days: []string{"fri"}, Nth: -1, From: "2026-10-17T16:51"}, "On the last Friday of each month at 17:00"},
		{Rule{At: []string{"08:00"}, Dates: []int{1, 15, 22, 23, 11}, From: "2026-10-17T16:51"}, "On the 1st, 11th, 15th, 22nd and 23rd of each month at 08:00"},
		{Rule{Once: "2026-10-20T14:00"}, "Once, Tue 20 Oct at 14:00"},
		{Rule{Once: "2027-01-04T09:00"}, "Once, Mon 4 Jan 2027 at 09:00"},
		{Rule{At: []string{"09:00"}, From: "2026-10-20T00:00", Until: "2026-12-31"}, "Every day at 09:00, starting Tue 20 Oct, until Thu 31 Dec"},
	} {
		rule := tc.rule
		if err := rule.Validate(); err != nil {
			t.Fatalf("%+v: %v", rule, err)
		}
		if got := (Definition{Message: "x", Rule: &rule}).Sentence(now); got != tc.want {
			t.Errorf("got %q want %q", got, tc.want)
		}
	}
	for _, tc := range []struct{ repeat, want string }{
		{"hourly", "Every hour from 00:30 to 23:30"},
		{"daily", "Every day at 09:30"},
		{"weekdays", "Weekdays at 09:30"},
		{"weekly", "Fridays at 09:30"},
		{"once", "Once, Fri 16 Oct at 09:30"},
	} {
		at := "2026-10-16T09:30"
		if tc.repeat == "hourly" {
			at = "2026-10-16T00:30"
		}
		if got := (Definition{Message: "x", Repeat: tc.repeat, At: at}).Sentence(now); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.repeat, got, tc.want)
		}
	}
}

func TestNormalRulesAreEqualWhenTheyRunAlike(t *testing.T) {
	a := Rule{At: []string{"9:00", "17:30", "09:00"}, Days: []string{"thu", "tue", "tue"}, From: "2026-10-17T16:51"}.Normal()
	b := Rule{At: []string{"17:30", "09:00"}, Days: []string{"tue", "thu"}, From: "2026-10-17T16:51"}.Normal()
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("%+v %+v", a, b)
	}
	if label := Label(time.Date(2027, 1, 4, 9, 5, 0, 0, time.UTC), time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)); label != "Mon 4 Jan 2027 09:05" {
		t.Fatal(label)
	}
}
