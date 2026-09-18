package engineer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	r.ElementsMatch([]string{"Carry more speed.", "Release earlier."}, host.said())
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
