package engineer

import (
	"encoding/json"

	"github.com/pacenote-sim/protocol/wire"
)

// The document the host hands over as the client sent it: the car setup on a
// stint. The host does not read it and does not know what is in it — that is
// the arrangement, and it is what lets a client send something new without the
// server being edited. The corners a cue is written from do not come this way
// at all: the client posts them to this plugin's own route, in this plugin's
// own shape (contract.go).
//
// This plugin reads the basic schema, which the protocol module publishes as
// its wire types. A document it cannot read is treated as absent rather than as
// an error: the stint still finished and the rest of the facts are still good.
// What it must never do is read half a document and present it as the whole.

// setupOf is the stint's car setup, or nil when the simulator published none
// or the document is not one this plugin reads.
func setupOf(doc json.RawMessage) *wire.CarSetup {
	if len(doc) == 0 {
		return nil
	}
	var out wire.CarSetup
	if err := json.Unmarshal(doc, &out); err != nil {
		return nil
	}
	return &out
}

// corner is one corner as this plugin reads it: the protocol's basic schema
// plus the measurements the client plugin posts to this plugin's own route.
type corner struct {
	// Turn is the corner's number on this lap, from 1, in the order driven.
	Turn int `json:"turn"`
	// ApexPct is where the apex is, in ‰ of the lap (0…1000).
	ApexPct int `json:"apex_pct"`
	// ApexKmh is the speed at the apex and RefApexKmh the speed the reference
	// lap carried at the same point. A RefApexKmh of zero means the client
	// compared against nothing there.
	ApexKmh    int `json:"apex_kmh"`
	RefApexKmh int `json:"ref_apex_kmh,omitempty"`
	// DeficitKmh is how much apex speed this corner lost against the
	// reference, in whole km/h. It is positive: a client sends the corners
	// that cost time and not the ones that gained it.
	//
	// It is a speed and not a time, because a speed is what is measured. A
	// per-corner time loss would be an integration over a piece of track that
	// the two laps did not necessarily cover at the same points, and calling
	// the result milliseconds would dress an estimate up as a measurement.
	DeficitKmh int `json:"deficit_kmh"`
	// BrakeAtApex is the brake still applied at the apex, in percent. A high
	// value is the driver trail-braking past the apex.
	BrakeAtApex int `json:"brake_at_apex,omitempty"`
	// ThrottleLag is the distance between the apex and the throttle pickup, in
	// ‰ of the lap. A high value is the car not rotated and the driver waiting.
	ThrottleLag int `json:"throttle_lag,omitempty"`
	// Pattern is the shape of the mistake, when the two channels above name
	// one. Empty is normal and means nothing conclusive.
	Pattern wire.CornerPattern `json:"pattern,omitempty"`
	// The eleven measurements the coach reads and the client plugin sends.
	BrakeAtPct     int `json:"brake_at_pct,omitempty"`
	RefBrakeAtPct  int `json:"ref_brake_at_pct,omitempty"`
	PeakBrakePct   int `json:"peak_brake_pct,omitempty"`
	TurnInBrakePct int `json:"turn_in_brake_pct,omitempty"`
	RefThrottleLag int `json:"ref_throttle_lag,omitempty"`
	MinKmh         int `json:"min_kmh,omitempty"`
	RefMinKmh      int `json:"ref_min_kmh,omitempty"`
	ExitKmh        int `json:"exit_kmh,omitempty"`
	RefExitKmh     int `json:"ref_exit_kmh,omitempty"`
	GearAtApex     int `json:"gear_at_apex,omitempty"`
	RefGearAtApex  int `json:"ref_gear_at_apex,omitempty"`
}
