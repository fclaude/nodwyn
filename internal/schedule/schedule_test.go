package schedule

import (
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
