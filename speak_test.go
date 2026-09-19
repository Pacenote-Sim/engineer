package engineer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pacenote-sim/plugin"
)

// fakeHost is the server as engineer may ask it, under the test's control.
type fakeHost struct {
	mu       sync.Mutex
	asked    []string
	payloads []string
	kinds    []string
	fail     error
	audio    []byte
}

func (h *fakeHost) Ask(_ context.Context, kind string, payload json.RawMessage) (json.RawMessage, error) {
	var req struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(payload, &req)
	h.mu.Lock()
	h.asked = append(h.asked, req.Text)
	h.payloads = append(h.payloads, string(payload))
	h.kinds = append(h.kinds, kind)
	h.mu.Unlock()
	if h.fail != nil {
		return nil, h.fail
	}
	out, _ := json.Marshal(map[string]any{"audio": h.audio, "content_type": "audio/mpeg", "cached": false})
	return out, nil
}

func (h *fakeHost) said() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.asked...)
}

func TestTheLinesComeWithTheirAudio(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, _ := vendorSaying(t, lapAnswer{Cues: []turnLine{
		{Turn: 9, Line: "Carry more speed."},
		{Turn: 1, Line: "Release earlier."},
	}})
	host := &fakeHost{audio: []byte("ID3audio")}
	e.Connected(host)

	res := postReport(t, e, aReport())
	r.Equal(http.StatusOK, res.Status, string(res.Body))
	got := cuesOf(t, res)
	r.Len(got.Cues, 2)
	for _, c := range got.Cues {
		r.Equal([]byte("ID3audio"), c.Audio, "a line went without its audio")
		r.Equal("audio/mpeg", c.AudioType)
	}
	r.Contains(string(res.Body), `"audio":"SUQzYXVkaW8="`, "the audio is not base64 in the answer")
	r.ElementsMatch([]string{"Turn 9. Carry more speed.", "Turn 1. Release earlier."}, host.said(), "every spoken line names its corner first")
	r.Equal([]string{"voice.speak", "voice.speak"}, host.kinds)

	// The lap's own cost is the model's; voice's is charged to voice by the host.
	r.Equal("cues", res.Usage.Job)
}

func TestWithoutAVoiceTheWordsGoAlone(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		fail   error
		logged string
	}{
		{"voice is not installed", plugin.ErrUnavailable, "no voice plugin is running"},
		{"voice is not configured", plugin.ErrNotConfigured, "not configured"},
		{"voice refused", errors.New("cartesia: 401 invalid api key"), "could not be spoken"},
		{"the manifest does not allow it", plugin.ErrNotAllowed, "does not let it ask voice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)
			e, out, _ := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "Release earlier."}}})
			e.Connected(&fakeHost{fail: tc.fail})
			res := postReport(t, e, aReport())
			r.Equal(http.StatusOK, res.Status, "a line without audio failed the lap")
			got := cuesOf(t, res)
			r.Len(got.Cues, 1)
			r.Empty(got.Cues[0].Audio)
			r.NotContains(string(res.Body), `"audio"`)
			r.Contains(out.String(), tc.logged)
		})
	}

	t.Run("no host at all", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		e, _, _ := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "Release earlier."}}})
		res := postReport(t, e, aReport())
		r.Equal(http.StatusOK, res.Status)
		r.Empty(cuesOf(t, res).Cues[0].Audio)
	})

	t.Run("an answer with no audio in it", func(t *testing.T) {
		t.Parallel()
		r := require.New(t)
		e, out, _ := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "Release earlier."}}})
		e.Connected(&fakeHost{})
		res := postReport(t, e, aReport())
		r.Empty(cuesOf(t, res).Cues[0].Audio)
		r.Contains(out.String(), "no audio")
	})
}

func TestSpeakingCanBeTurnedOff(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, _ := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "Release earlier."}}})
	host := &fakeHost{audio: []byte("audio")}
	e.Connected(host)
	res := postReport(t, e, aReport(), func(q *plugin.HTTPRequest) { q.Settings[SettingSpeak] = "false" })
	r.Equal(http.StatusOK, res.Status)
	r.Empty(cuesOf(t, res).Cues[0].Audio)
	r.Empty(host.said(), "voice was asked with speaking turned off")
}

// The radio line is spoken too, and filed with its audio — which needs a
// store; without one the line has nobody to reach and voice is not asked.
func TestTheRadioLineIsSpokenOnlyWhenItCanBeFiled(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	e, _, _ := vendorSaying(t, cueAnswer{Line: "P4, car behind at eight tenths."})
	host := &fakeHost{audio: []byte("audio")}
	e.Connected(host)
	_, err := e.Notify(t.Context(), raceEvent("race-1", 3, &plugin.Position{ClassPos: 5}, 400))
	r.NoError(err)
	_, err = e.Notify(t.Context(), raceEvent("race-1", 4, &plugin.Position{ClassPos: 4}, 400))
	r.NoError(err)
	r.Empty(host.said(), "voice was paid for a line nobody can fetch")
}

// Audio is bought by the character, so a lap buys it for the corners that lost
// the most and lets the rest travel as words. How many is the operator's,
// because it is the operator's bill.
func TestOnlyTheWorstCornersAreSpokenAloud(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	// Four corners, worst first as the client ranked them by what they cost.
	rep := aReport()
	rep.Corners = []corner{
		{Turn: 4, ApexPct: 400, ApexKmh: 60, RefApexKmh: 80, DeficitKmh: 20},
		{Turn: 2, ApexPct: 200, ApexKmh: 70, RefApexKmh: 85, DeficitKmh: 15},
		{Turn: 7, ApexPct: 700, ApexKmh: 90, RefApexKmh: 98, DeficitKmh: 8},
		{Turn: 9, ApexPct: 900, ApexKmh: 95, RefApexKmh: 97, DeficitKmh: 2},
	}
	answer := lapAnswer{Cues: []turnLine{
		{Turn: 4, Line: "Brake later."},
		{Turn: 2, Line: "Carry more speed."},
		{Turn: 7, Line: "Get on the power earlier."},
		{Turn: 9, Line: "Tidy the exit."},
	}}

	e, _, _ := vendorSaying(t, answer)
	host := &fakeHost{audio: []byte("clip")}
	e.Connected(host)
	res := postReport(t, e, rep)
	r.Equal(http.StatusOK, res.Status, string(res.Body))
	got := cuesOf(t, res)
	r.Len(got.Cues, 4, "every line is written; only some are spoken")
	r.Len(host.said(), DefaultAudioLines, "two lines a lap, not four")
	spoken := 0
	for _, c := range got.Cues {
		if len(c.Audio) > 0 {
			spoken++
		}
	}
	r.Equal(DefaultAudioLines, spoken)
	said := strings.Join(host.said(), " | ")
	r.Contains(said, "Turn 4", "the corner that lost the most buys audio")
	r.Contains(said, "Turn 2", "and the next worst")
	r.NotContains(said, "Turn 7", "the rest go as words")
	r.NotContains(said, "Turn 9")

	// The setting moves it, up to a lap's lines and down to none at all.
	for _, tc := range []struct {
		set  string
		want int
	}{{"0", 0}, {"1", 1}, {"4", 4}, {"9", MaxAudioLines}, {"", DefaultAudioLines}, {"-2", DefaultAudioLines}} {
		e2, _, _ := vendorSaying(t, answer)
		h2 := &fakeHost{audio: []byte("clip")}
		e2.Connected(h2)
		res = postReport(t, e2, rep, func(q *plugin.HTTPRequest) {
			if tc.set == "" {
				delete(q.Settings, SettingAudio)
				return
			}
			q.Settings[SettingAudio] = tc.set
		})
		r.Equal(http.StatusOK, res.Status)
		r.Len(h2.said(), tc.want, "audio_lines %q", tc.set)
	}
}

// The Turn 1 line sent a lap ahead is insurance: the full lap writes Turn 1
// again with the whole lap in view and that is the line usually heard. So the
// early one is written and filed, and no audio is bought for it.
func TestTheEarlyTurnOneLineIsNotSpokenFor(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	early := lapReport{
		StintID: "stint-1", Lap: 4, Session: plugin.SessionPractice, TrackLengthM: spa, Early: true,
		Corners: []corner{{Turn: 1, ApexPct: 44, ApexKmh: 44, RefApexKmh: 58, DeficitKmh: 14}},
	}
	e, _, _ := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "Brake ten metres later."}}})
	host := &fakeHost{audio: []byte("clip")}
	e.Connected(host)

	res := postReport(t, e, early)
	r.Equal(http.StatusOK, res.Status, string(res.Body))
	got := cuesOf(t, res)
	r.Len(got.Cues, 1, "the line is still written and still sent")
	r.Empty(got.Cues[0].Audio, "and it is not paid to be spoken")
	r.Empty(host.said(), "voice was asked for a line that is usually replaced")

	// The same report without the flag buys its audio as any lap does.
	full := early
	full.Early = false
	e2, _, _ := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "Brake ten metres later."}}})
	host2 := &fakeHost{audio: []byte("clip")}
	e2.Connected(host2)
	res = postReport(t, e2, full)
	r.Equal(http.StatusOK, res.Status)
	r.NotEmpty(cuesOf(t, res).Cues[0].Audio)
	r.Len(host2.said(), 1)
}

// A client that will not play what it is sent says so, and no audio is bought
// for it: the lines are written and filed as always and the driver reads them.
func TestAClientThatHearsNothingIsNotSpokenFor(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	silent := aReport()
	silent.Silent = true
	e, _, _ := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "Release earlier."}}})
	host := &fakeHost{audio: []byte("clip")}
	e.Connected(host)

	res := postReport(t, e, silent)
	r.Equal(http.StatusOK, res.Status, string(res.Body))
	got := cuesOf(t, res)
	r.Len(got.Cues, 1, "the lines are still written")
	r.Empty(got.Cues[0].Audio)
	r.Empty(host.said(), "voice was asked for a driver who is reading")

	// The same client with its sound on buys its audio as any lap does.
	loud := silent
	loud.Silent = false
	e2, _, _ := vendorSaying(t, lapAnswer{Cues: []turnLine{{Turn: 1, Line: "Release earlier."}}})
	host2 := &fakeHost{audio: []byte("clip")}
	e2.Connected(host2)
	res = postReport(t, e2, loud)
	r.Equal(http.StatusOK, res.Status)
	r.NotEmpty(cuesOf(t, res).Cues[0].Audio)
	r.Len(host2.said(), 1)
}
