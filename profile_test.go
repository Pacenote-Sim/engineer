package engineer

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/plugin"
)

// The reading of a driver, without a database. What needs one is in
// store_postgres_test.go.

func TestCleanWeaknessesKeepsWhatWasAskedFor(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	got := cleanWeaknesses([]Weakness{
		{Area: " Slow corners ", Evidence: " Fourteen down at Turn 1 twice. ", Trend: " WORSE "},
		{Area: "", Evidence: "", Trend: "improving"},
		{Area: "Consistency", Evidence: "82 then 79.", Trend: "getting there"},
		{Area: "Braking", Evidence: "Still 12 percent at the apex.", Trend: "new"},
		{Area: "A fourth", Evidence: "x", Trend: "steady"},
	})
	r.Len(got, MaxWeaknesses, "an empty weakness was kept, or more than three were")
	r.Equal("Slow corners", got[0].Area)
	r.Equal("Fourteen down at Turn 1 twice.", got[0].Evidence)
	r.Equal("worse", got[0].Trend, "a trend was not normalised")
	r.Equal("steady", got[1].Trend, "a trend that is not one of the four was kept")
	r.Equal("new", got[2].Trend)
}

func TestProfileFactsReadsOldestFirst(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	newer := Debrief{
		DriverName: "Mihai Racovita", Track: "Spa", Car: "GT3", Summary: "Better.",
		Findings:  []Finding{{Area: "Turn 1", Observation: "Ten down.", Drill: "Release earlier."}},
		WrittenAt: time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC),
	}
	older := Debrief{
		DriverName: "Mihai", Track: "Monza", Summary: "Fourteen down at Turn 1.",
		WrittenAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
	}
	got := profileFacts([]Debrief{newer, older})
	r.True(strings.HasPrefix(got, "Mihai Racovita. 2 debrief(s), oldest first.\n"), got)
	r.Less(strings.Index(got, "1 Sep 2026"), strings.Index(got, "14 Sep 2026"), "not oldest first")
	r.Contains(got, "Monza, an unnamed car.")
	r.Contains(got, "- Turn 1: Ten down. Release earlier.")

	r.Contains(profileFacts(nil), "The driver. 0 debrief(s)")
}

func TestTheReadingIsRenderedAndEscaped(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.Empty(profileCard(Profile{}, false), "an empty reading rendered a card")

	html := profileCard(Profile{
		Summary:    `Quick <script>alert(1)</script>`,
		Weaknesses: []Weakness{{Area: "<b>", Evidence: `x" onload="y`, Trend: "worse"}},
		Debriefs:   3, UpdatedAt: time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC),
	}, true)
	r.NotContains(html, "<script>")
	r.NotContains(html, `onload="`)
	r.Contains(html, "&lt;script&gt;")
	r.Contains(html, `<span class="pill">worse</span>`)
	r.Contains(html, "Read from 3 debrief(s)")
	r.Contains(html, "a newer debrief since", "a stale reading does not say so")
}

func TestTheOverviewAndTheDriverPageWithoutADatabase(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	e := New(nil, nil)

	res, err := e.ServeHTTP(context.Background(), plugin.HTTPRequest{Method: http.MethodGet, Path: "/"})
	r.NoError(err)
	r.Equal(http.StatusOK, res.Status)
	r.Contains(string(res.Body), "no database")

	res, err = e.ServeHTTP(context.Background(), plugin.HTTPRequest{Method: http.MethodGet, Path: "/drivers/mihai"})
	r.NoError(err)
	r.Equal(http.StatusOK, res.Status)
	r.Contains(string(res.Body), "no database")

	// A slug that is not one is nobody, whatever the database says.
	res, err = e.ServeHTTP(context.Background(), plugin.HTTPRequest{Method: http.MethodGet, Path: "/drivers/Not A Slug"})
	r.NoError(err)
	r.Equal(http.StatusNotFound, res.Status)
}
