package engineer

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/plugin"
)

// raceEvent is a race lap as the server delivers it: lap n of a stint, at a
// position, so far off the reference.
func raceEvent(stint string, lap int, pos *plugin.Position, deltaMs int) plugin.Event {
	facts := aLap()
	facts.Number, facts.DeltaMs, facts.Position = lap, deltaMs, pos
	return plugin.Event{
		Kind:     plugin.EventLapCompleted,
		Driver:   plugin.Driver{Slug: "mihai", Name: "Mihai"},
		Session:  plugin.Session{StintID: stint, Type: plugin.SessionRace, Track: "Spa", Car: "GT3"},
		Lap:      facts,
		Settings: settings(),
		Secrets:  secrets(),
	}
}

func TestChangesAreTheOnlyReasonToSpeak(t *testing.T) {
	t.Parallel()

	prev := &raceLap{Number: 3, DeltaMs: 400, Reference: "your best lap", ClassPos: 5, GapAheadMs: 3400, GapBehindMs: 1900}
	same := func() *plugin.LapFacts {
		l := aLap()
		l.DeltaMs = 450
		l.Position = &plugin.Position{ClassPos: 5, GapAheadMs: 3300, GapBehindMs: 1800}
		return l
	}

	for _, tc := range []struct {
		name string
		prev *raceLap
		lap  func() *plugin.LapFacts
		want []string
	}{
		{"the first lap of a stint", nil, same, nil},
		{"nothing changed", prev, same, nil},
		{"a personal best on the first lap is not one yet", nil, func() *plugin.LapFacts {
			l := same()
			l.PersonalBest = true
			return l
		}, nil},
		{"a personal best", prev, func() *plugin.LapFacts {
			l := same()
			l.PersonalBest = true
			return l
		}, []string{"This is the driver's best lap here."}},
		{"a position gained", prev, func() *plugin.LapFacts {
			l := same()
			l.Position.ClassPos = 4
			return l
		}, []string{"Position changed from P5 to P4."}},
		{"the car behind arrives", prev, func() *plugin.LapFacts {
			l := same()
			l.Position.GapBehindMs = 800
			return l
		}, []string{"The car behind has closed to 0.80 seconds."}},
		{"the car ahead is caught", prev, func() *plugin.LapFacts {
			l := same()
			l.Position.GapAheadMs = 900
			return l
		}, []string{"The car ahead is now 0.90 seconds away."}},
		{"a fight that was already on is not news", &raceLap{ClassPos: 5, GapBehindMs: 700, Reference: "your best lap", DeltaMs: 400}, func() *plugin.LapFacts {
			l := same()
			l.Position.GapBehindMs = 600
			return l
		}, nil},
		{"half a second lost on a clean lap", prev, func() *plugin.LapFacts {
			l := same()
			l.DeltaMs = 900
			return l
		}, []string{"The lap was 0.50 seconds slower than the one before."}},
		{"a slow in-lap is expected", prev, func() *plugin.LapFacts {
			l := same()
			l.DeltaMs = 9000
			l.Kind = plugin.LapIn
			return l
		}, nil},
		{"a different reference is not a comparison", &raceLap{DeltaMs: 400, Reference: "the class best", ClassPos: 5}, func() *plugin.LapFacts {
			l := same()
			l.DeltaMs = 900
			return l
		}, nil},
		{"a position the server did not know last lap", &raceLap{Reference: "your best lap", DeltaMs: 400}, func() *plugin.LapFacts {
			l := same()
			l.Position.ClassPos = 4
			return l
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, changes(tc.prev, tc.lap()))
		})
	}
}

func TestTheRadioSpeaksOnlyWhenSomethingChanged(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, asked := vendorSaying(t, cueAnswer{Line: "P4, car behind at eight tenths."})

	// The first lap: nothing to compare with, nothing said, nothing spent.
	use, err := e.Notify(t.Context(), raceEvent("race-1", 3, &plugin.Position{ClassPos: 5, GapAheadMs: 3400, GapBehindMs: 1900}, 400))
	r.NoError(err)
	r.Zero(use.Total())
	r.Empty(*asked)

	// The next lap, a position gained: one call, twelve words at most.
	use, err = e.Notify(t.Context(), raceEvent("race-1", 4, &plugin.Position{ClassPos: 4, GapAheadMs: 2100, GapBehindMs: 800}, 450))
	r.NoError(err)
	r.Equal("racepace", use.Job)
	r.EqualValues(420, use.Total())
	r.Contains(*asked, "Position changed from P5 to P4.")
	r.Contains(*asked, "The car behind has closed to 0.80 seconds.")
	r.Contains(*asked, "Last lap: P5 in class, 3.40 seconds behind the car ahead, 1.90 seconds ahead of the car behind; 0.40 seconds slower against your best lap.")
	r.Contains(*asked, "Race: P4 in class, 2.10 seconds behind the car ahead, 0.80 seconds ahead of the car behind.")
	r.Contains(*asked, "on the radio during a race")
	r.NotContains(*asked, "These are the only turns", "the radio was shown corners to name")

	// And a lap where nothing moved is silence again.
	*asked = ""
	use, err = e.Notify(t.Context(), raceEvent("race-1", 5, &plugin.Position{ClassPos: 4, GapAheadMs: 2000, GapBehindMs: 700}, 470))
	r.NoError(err)
	r.Zero(use.Total())
	r.Empty(*asked)
}

func TestTheRadioIsSilentOutsideARaceOrWhenOff(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, asked := vendorSaying(t, cueAnswer{Line: "P4."})

	practice := raceEvent("p-1", 3, &plugin.Position{ClassPos: 5}, 400)
	practice.Session.Type = plugin.SessionPractice
	_, err := e.Notify(t.Context(), practice)
	r.NoError(err)
	practice.Lap.Number, practice.Lap.Position.ClassPos = 4, 4
	use, err := e.Notify(t.Context(), practice)
	r.NoError(err)
	r.Zero(use.Total())
	r.Empty(*asked, "the radio spoke in practice")

	off := raceEvent("race-2", 3, &plugin.Position{ClassPos: 5}, 400)
	off.Settings[SettingRacePace] = "false"
	_, err = e.Notify(t.Context(), off)
	r.NoError(err)
	off.Lap.Number, off.Lap.Position.ClassPos = 4, 4
	use, err = e.Notify(t.Context(), off)
	r.NoError(err)
	r.Zero(use.Total())
	r.Empty(*asked, "the radio spoke with the radio turned off")
}

// A line that breaks a rule is dropped, the operator is told, and the event is
// not a failure: the driver hears nothing, which is what silence is for.
func TestAnUnusableRadioLineIsDropped(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, line, rule string
	}{
		{"it names a turn", "Brake later into Turn 4.", "no corners to name one from"},
		{"it is too long", strings.TrimSpace(strings.Repeat("word ", 13)), "13 words"},
		{"it spells a unit as a symbol", "Gap 0.8s behind.", "symbol"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			e, out, _ := vendorSaying(t, cueAnswer{Line: tc.line})
			_, err := e.Notify(t.Context(), raceEvent("race-3", 3, &plugin.Position{ClassPos: 5}, 400))
			r.NoError(err)
			use, err := e.Notify(t.Context(), raceEvent("race-3", 4, &plugin.Position{ClassPos: 4}, 400))
			r.NoError(err, "a dropped line failed the event")
			r.EqualValues(420, use.Total(), "a dropped line was reported as free")
			r.Contains(out.String(), "a cue was discarded")
			r.Contains(out.String(), tc.rule)
		})
	}

	t.Run("it is empty", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		e, out, _ := vendorSaying(t, cueAnswer{Line: "  "})
		_, err := e.Notify(t.Context(), raceEvent("race-4", 3, &plugin.Position{ClassPos: 5}, 400))
		r.NoError(err)
		use, err := e.Notify(t.Context(), raceEvent("race-4", 4, &plugin.Position{ClassPos: 4}, 400))
		r.NoError(err)
		r.EqualValues(420, use.Total())
		r.NotContains(out.String(), "discarded", "saying nothing is not a discarded line")
	})

	t.Run("the vendor is down", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		e, out, _ := vendorReplying(t, 500, `{"error":{"message":"overloaded"}}`)
		_, err := e.Notify(t.Context(), raceEvent("race-5", 3, &plugin.Position{ClassPos: 5}, 400))
		r.NoError(err)
		_, err = e.Notify(t.Context(), raceEvent("race-5", 4, &plugin.Position{ClassPos: 4}, 400))
		r.NoError(err, "a vendor failure on the radio failed the event")
		r.Contains(out.String(), "nothing was said")
	})
}

// The key is needed only at the moment something is worth saying: a plugin with
// no key remembers laps for free and complains only when it would have spoken.
func TestTheRadioNeedsAKeyOnlyWhenItWouldSpeak(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, _ := vendorSaying(t, cueAnswer{Line: "P4."})
	quiet := raceEvent("race-6", 3, &plugin.Position{ClassPos: 5}, 400)
	quiet.Secrets = nil
	_, err := e.Notify(t.Context(), quiet)
	r.NoError(err)

	loud := raceEvent("race-6", 4, &plugin.Position{ClassPos: 4}, 400)
	loud.Secrets = nil
	_, err = e.Notify(t.Context(), loud)
	r.ErrorIs(err, plugin.ErrNotConfigured)
}

// A stint that finished is forgotten, so a stint id the host reuses — or a
// restart mid-race — starts from nothing rather than from a stale lap.
func TestAFinishedStintIsForgotten(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var rs races
	r.Nil(rs.remember("s", aLap()))
	r.NotNil(rs.remember("s", aLap()))
	rs.forget("s")
	r.Nil(rs.remember("s", aLap()), "a finished stint was still remembered")
	rs.forget("never-seen")
}

// What is remembered is bounded, for a host that never says a stint finished.
func TestWhatIsRememberedIsBounded(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var rs races
	for i := range maxRaces {
		rs.remember(fmt.Sprintf("stint-%d", i), aLap())
	}
	r.Len(rs.laps, maxRaces)
	// A stint already known is updated in place, not counted again.
	r.NotNil(rs.remember("stint-0", aLap()))
	r.Len(rs.laps, maxRaces)
	// One more stint and everything is forgotten rather than one thing.
	r.Nil(rs.remember("one-more", aLap()))
	r.Len(rs.laps, 1)
}

// Every method is called on its own goroutine and several may be in flight at
// once: a full grid finishing laps together.
func TestRememberingIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	var rs races
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stint := fmt.Sprintf("stint-%d", i%4)
			rs.remember(stint, aLap())
			rs.forget(stint)
		}()
	}
	wg.Wait()
}
