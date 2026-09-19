package engineer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/plugin"
)

// The contract is read leniently: a field this plugin does not know is ignored,
// because the client measuring something new must not break the coach that
// predates it.
func TestAReportIsReadLeniently(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rep, err := parseLapReport([]byte(`{
		"stint_id": "stint-1", "lap": 3, "session": "qualifying", "track_length_m": 7004,
		"something_new": true,
		"corners": [{"turn": 4, "apex_pct": 310, "brake_at_pct": 288, "ref_brake_at_pct": 296, "another_new_one": 1}]
	}`))
	r.NoError(err)
	r.Equal("stint-1", rep.StintID)
	r.Equal(plugin.SessionQualifying, rep.Session)
	r.Len(rep.Corners, 1)
	r.Equal(288, rep.Corners[0].BrakeAtPct)
	r.Equal([]int{4}, rep.turns())

	// A report of a lap that lost time nowhere is a report.
	rep, err = parseLapReport([]byte(`{"stint_id": "stint-1", "lap": 3, "session": "practice"}`))
	r.NoError(err)
	r.Empty(rep.Corners)
	r.Empty(rep.turns())
}

func TestWhatIsNotAnIdentifier(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	r.True(identifier("7f0c2e1a-3b4d-4e5f-8a9b-0c1d2e3f4a5b"))
	r.True(identifier("stint_1"))
	r.False(identifier("two words"))
	r.False(identifier("tab\there"))
	r.False(identifier("nul\x00"))
}

// What several jobs wrote about one lap comes back to the client as one answer,
// in the order the lines are heard: corners in track order, the radio last.
func TestMergedOrdersLinesAsHeard(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	early := time.Date(2026, 9, 14, 10, 0, 0, 0, time.UTC)
	late := early.Add(time.Minute)
	rows := []LapCues{
		{
			Job: jobRacePace, StintID: "s", Lap: 7, Mode: CueRace,
			Cues: []cueLine{{Line: "P4, car behind at eight tenths."}}, WrittenAt: late,
		},
		{
			Job: jobCues, StintID: "s", Lap: 7, Mode: CueRace, Reference: "your best lap",
			Cues:       []cueLine{{Turn: 9, ApexPct: 610, Line: "Carry more speed."}, {Turn: 1, ApexPct: 44, Line: "Release earlier."}},
			SetupNotes: []string{"The front washes out."},
			WrittenAt:  early,
		},
	}
	got := merged(rows)
	r.Equal("s", got.StintID)
	r.Equal(7, got.Lap)
	r.Len(got.Cues, 3)
	r.Equal(1, got.Cues[0].Turn)
	r.Equal(9, got.Cues[1].Turn)
	r.Equal(0, got.Cues[2].Turn, "the radio line is not last")
	r.Equal([]string{"The front washes out."}, got.SetupNotes)
	r.Equal(late, got.WrittenAt, "the newest writing is not the one stamped")

	// Nothing is an empty list, never null.
	none := merged(nil)
	r.NotNil(none.Cues)
	r.Empty(none.Cues)

	// Two corners at the same point on the lap keep their turn order.
	tie := []cueLine{{Turn: 5, ApexPct: 300}, {Turn: 4, ApexPct: 300}}
	sortCues(tie)
	r.Equal(4, tie[0].Turn)
}

// A throttle lag that cannot be true of a corner is dropped rather than
// spoken: the client's arithmetic wrapped, and three kilometres between an
// apex and a throttle is not something to tell a driver.
func TestAnImpossibleThrottleLagIsDropped(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	body := []byte(`{"stint_id":"s1","lap":3,"session":"practice","track_length_m":3650,"corners":[
		{"turn":1,"apex_pct":100,"apex_kmh":80,"deficit_kmh":5,"throttle_lag":982,"ref_throttle_lag":40},
		{"turn":2,"apex_pct":300,"apex_kmh":90,"deficit_kmh":4,"throttle_lag":60,"ref_throttle_lag":-3},
		{"turn":3,"apex_pct":500,"apex_kmh":70,"deficit_kmh":6,"throttle_lag":200}]}`)
	rep, err := parseLapReport(body)
	r.NoError(err)
	r.Zero(rep.Corners[0].ThrottleLag, "982 thousandths of a lap is not an exit")
	r.Equal(40, rep.Corners[0].RefThrottleLag)
	r.Equal(60, rep.Corners[1].ThrottleLag)
	r.Zero(rep.Corners[1].RefThrottleLag, "nor is a negative one")
	r.Equal(MaxLagPct, rep.Corners[2].ThrottleLag, "at the bound it is still a measurement")

	// And nothing about it reaches the coach.
	r.NotContains(reportFacts(rep, nil), "3585")
	r.NotContains(reportFacts(rep, nil), "throttle 982")
}
