package engineer

import (
	"encoding/json"
	"testing"

	"github.com/pacenote-sim/plugin"
)

// What a lap costs this plugin besides the model. The model call is seconds and
// is the vendor's; everything around it — reading the report, rendering the
// facts, checking the lines, ordering them — is this plugin's, and it runs once
// a lap for every driver on the grid. None of it should register against the
// host's 15-second deadline, and these are how that stays true.

// fortyCorners is the largest report the contract accepts.
func fortyCorners() lapReport {
	rep := aReport()
	rep.Corners = nil
	for i := 1; i <= MaxCornersPerLap; i++ {
		rep.Corners = append(rep.Corners, corner{
			Turn: i, ApexPct: i * 24, ApexKmh: 90 + i, RefApexKmh: 95 + i, DeficitKmh: 5,
			BrakeAtPct: i*24 - 10, RefBrakeAtPct: i*24 - 12, PeakBrakePct: 90, TurnInBrakePct: 70,
			BrakeAtApex: 8, ThrottleLag: 12, RefThrottleLag: 6, GearAtApex: 3, RefGearAtApex: 3,
		})
	}
	return rep
}

func BenchmarkReadingAFullReport(b *testing.B) {
	body, err := json.Marshal(fortyCorners())
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := parseLapReport(body); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRenderingTheFactsForAFullReport(b *testing.B) {
	rep := fortyCorners()
	rep.SetupOpen = true
	notes := []string{"The front washes out mid-corner.", "The rear steps out on the throttle."}
	b.ReportAllocs()
	for b.Loop() {
		_ = reportFacts(rep, notes)
	}
}

func BenchmarkCheckingALine(b *testing.B) {
	const line = "Brake twenty metres later into Turn 4 and ease to seventy-five percent by turn-in."
	b.ReportAllocs()
	for b.Loop() {
		if err := ValidateCornerLine("en", CueTraining, line, 4); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkOrderingALapsLines(b *testing.B) {
	rep := fortyCorners()
	rows := []LapCues{{Job: jobRacePace, Cues: []cueLine{{Line: "P4."}}}, {Job: jobCues}}
	for i := len(rep.Corners) - 1; i >= 0; i-- {
		c := rep.Corners[i]
		rows[1].Cues = append(rows[1].Cues, cueLine{Turn: c.Turn, ApexPct: c.ApexPct, Line: "Later."})
	}
	b.ReportAllocs()
	for b.Loop() {
		_ = merged(rows)
	}
}

func BenchmarkDecidingWhetherTheRadioSpeaks(b *testing.B) {
	prev := &raceLap{Number: 3, DeltaMs: 400, Reference: "your best lap", ClassPos: 5, GapAheadMs: 3400, GapBehindMs: 1900}
	lap := aLap()
	lap.Position = &plugin.Position{ClassPos: 4, GapAheadMs: 2100, GapBehindMs: 800}
	b.ReportAllocs()
	for b.Loop() {
		_ = changes(prev, lap)
	}
}
