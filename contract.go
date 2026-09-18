package engineer

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/pacenote-sim/plugin"
)

// What a client plugin posts to this plugin, and what it gets back.
//
// This is the one contract this plugin owns. The server carries a lap's basic
// corner analysis as a document it does not read; what a coach needs — where
// the braking started and how hard, how it was released, where the throttle
// came back — is more than the protocol's corner, and the server's decoder
// refuses fields it does not know. So the client that measured them posts them
// here, on this plugin's own address, and the server is not edited.
//
// It is JSON, it is documented in the README, and unknown fields are ignored: a
// client that measures something new tomorrow must not break the coach it is
// talking to today.

// MaxCornersPerLap bounds a report. The longest circuits anyone races have
// about thirty corners; forty leaves room and stops a report being a payload.
const MaxCornersPerLap = 40

// MaxSetupNotes is how many observations about the car one lap may add.
const MaxSetupNotes = 2

// maxNoteRunes bounds one observation about the car. It is a clause, not a
// paragraph, and a model that writes a paragraph is a model that was not told.
const maxNoteRunes = 200

// lapReport is one lap as the client measured it: the corners that lost time
// against a reference, and what the coach needs in order to say so.
type lapReport struct {
	// StintID is the server's own identifier for the stint, which the client
	// has from its upload. It is how the lap's cues are filed and fetched.
	StintID string `json:"stint_id"`
	// Lap is the simulator's lap counter.
	Lap int `json:"lap"`
	// LapMs is the lap time, and SpokenLap the client's own rendering of it —
	// "one minute 38.4 seconds" — which a model cannot be trusted to produce.
	LapMs     int    `json:"lap_ms,omitempty"`
	SpokenLap string `json:"spoken_lap,omitempty"`
	// Session is what the driver is doing. A race gets the shorter lines.
	Session plugin.SessionType `json:"session"`
	// TrackLengthM is the circuit length in metres, which turns a thousandth
	// of a lap into a distance a driver can act on. Zero means no distances.
	TrackLengthM int `json:"track_length_m,omitempty"`
	// Reference names what every corner was compared against, in words a
	// driver would use: "the team's best lap in this car". Empty means the
	// client compared against nothing.
	Reference string `json:"reference,omitempty"`
	// DeltaMs is the lap time against the reference: positive is slower.
	DeltaMs int `json:"delta_ms,omitempty"`
	// SetupOpen reports that the car can be changed in this session. When it
	// can, the coach notes what the car is doing, lap by lap, for the setup
	// advice after the stint; on a fixed setup it says nothing about the car.
	SetupOpen bool `json:"setup_open,omitempty"`
	// Corners are the corners that lost time against the reference, worst
	// first, with the measurements the coach turns into words.
	Corners []corner `json:"corners"`
}

// parseLapReport reads a report and refuses one that cannot be coached from.
func parseLapReport(body []byte) (lapReport, error) {
	var rep lapReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return lapReport{}, fmt.Errorf("the report is not readable JSON: %w", err)
	}
	if err := rep.validate(); err != nil {
		return lapReport{}, err
	}
	return rep, nil
}

func (rep lapReport) validate() error {
	switch {
	case strings.TrimSpace(rep.StintID) == "":
		return errors.New("stint_id is required: it is how the lap's cues are filed and fetched")
	case len(rep.StintID) > 64 || !identifier(rep.StintID):
		return errors.New("stint_id is not an identifier")
	case rep.Lap <= 0:
		return errors.New("lap must be the simulator's lap number, from 1")
	case !validSession(rep.Session):
		return errors.New("session must be practice, qualifying, race or testing")
	case rep.TrackLengthM < 0:
		return errors.New("track_length_m cannot be negative")
	case len(rep.Corners) > MaxCornersPerLap:
		return fmt.Errorf("%d corners is more than a lap has; the limit is %d", len(rep.Corners), MaxCornersPerLap)
	}
	seen := make(map[int]bool, len(rep.Corners))
	for i := range rep.Corners {
		c := &rep.Corners[i]
		switch {
		case c.Turn <= 0:
			return errors.New("every corner needs a turn number, from 1")
		case seen[c.Turn]:
			return fmt.Errorf("turn %d is listed twice", c.Turn)
		case c.ApexPct < 0 || c.ApexPct > 1000:
			return fmt.Errorf("turn %d has its apex outside the lap", c.Turn)
		}
		seen[c.Turn] = true
	}
	return nil
}

// turns is the turn numbers of a report, in the order given.
func (rep lapReport) turns() []int {
	out := make([]int, 0, len(rep.Corners))
	for i := range rep.Corners {
		out = append(out, rep.Corners[i].Turn)
	}
	return out
}

func validSession(s plugin.SessionType) bool {
	switch s {
	case plugin.SessionPractice, plugin.SessionQualifying, plugin.SessionRace, plugin.SessionTesting:
		return true
	default:
		return false
	}
}

// identifier is something that can be a key and a query parameter: printable,
// and without the whitespace that would make two spellings of one id.
func identifier(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// cueLine is one spoken line and the corner it is for. Turn zero is a line
// about the lap as a whole — the radio in a race — and belongs to no corner.
type cueLine struct {
	Turn int `json:"turn"`
	// ApexPct is where on the lap the corner is, in thousandths, so the client
	// knows when to speak the line without asking again.
	ApexPct int    `json:"apex_pct,omitempty"`
	Line    string `json:"line"`
	// Audio is the line spoken, when a voice plugin is installed and answered
	// — base64 in JSON — and AudioType what it is, "audio/mpeg" or "audio/wav".
	// Absent, the client speaks or shows the words itself.
	Audio     []byte `json:"audio,omitempty"`
	AudioType string `json:"audio_type,omitempty"`
}

// LapCues is what the coach said about one lap, as filed and as fetched.
type LapCues struct {
	ID         int64  `json:"-"`
	DriverSlug string `json:"-"`
	// Job is what wrote it: jobCues, one line per corner from a client's
	// report, or jobRacePace, one line on the radio from the server's own lap
	// facts. The two are filed apart and fetched together.
	Job string `json:"-"`

	StintID   string             `json:"stint_id"`
	Lap       int                `json:"lap"`
	Session   plugin.SessionType `json:"session,omitempty"`
	Mode      CueKind            `json:"mode,omitempty"`
	Reference string             `json:"reference,omitempty"`
	// Cues are the lines, in the order they are heard.
	Cues []cueLine `json:"cues"`
	// SetupNotes is what this lap added about the car, when the setup is open.
	SetupNotes []string `json:"setup_notes,omitempty"`

	Model     string    `json:"model,omitempty"`
	Prompt    string    `json:"prompt_version,omitempty"`
	WrittenAt time.Time `json:"written_at"`
}

// merged folds what several jobs wrote about one lap into one answer for the
// client: the corner lines in track order, then the radio line, and whatever
// the lap noted about the car.
func merged(rows []LapCues) LapCues {
	if len(rows) == 0 {
		return LapCues{Cues: []cueLine{}}
	}
	out := rows[0]
	out.Cues, out.SetupNotes = []cueLine{}, nil
	for i := range rows {
		r := &rows[i]
		out.Cues = append(out.Cues, r.Cues...)
		out.SetupNotes = append(out.SetupNotes, r.SetupNotes...)
		if r.WrittenAt.After(out.WrittenAt) {
			out.WrittenAt = r.WrittenAt
		}
	}
	sortCues(out.Cues)
	return out
}

// sortCues puts lines in the order they are heard: by where on the lap they
// are, with a line about the whole lap last.
func sortCues(cues []cueLine) {
	sort.SliceStable(cues, func(i, j int) bool {
		a, b := cues[i], cues[j]
		if (a.Turn == 0) != (b.Turn == 0) {
			return a.Turn != 0
		}
		if a.ApexPct != b.ApexPct {
			return a.ApexPct < b.ApexPct
		}
		return a.Turn < b.Turn
	})
}
