package engineer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/pacenote-sim/plugin"
)

// The lines, spoken.
//
// Engineer writes words. When a plugin called voice is installed, it asks it
// to speak every line it writes, and the client gets the words and the audio
// in one answer — one round trip a lap, the audio ready when the words are.
// Without voice, or when voice is down, the words go alone and the client
// speaks them itself or shows them. Nothing about a lap fails over its audio.

// askVoice is the plugin asked, and what it is asked for. Any plugin named
// voice that answers voice.speak will do; this one knows nothing about which.
const (
	askVoice   = "voice"
	kindSpeak  = "voice.speak"
	speakEach  = 4 * time.Second
	audioLines = 24
)

// Connected is the host handing this plugin the way to ask other plugins. It
// is called once, before Settings, and kept.
func (e *Engineer) Connected(host plugin.Host) {
	e.hostMu.Lock()
	e.host = host
	e.hostMu.Unlock()
}

func (e *Engineer) askable() plugin.Host {
	e.hostMu.RLock()
	defer e.hostMu.RUnlock()
	return e.host
}

// spoken is what voice answers with: the audio and what it is. Only the two
// fields this plugin passes on are read; the rest of voice's answer is its own.
type spoken struct {
	Audio       []byte `json:"audio"`
	ContentType string `json:"content_type"`
}

// speakLines asks voice for every line, at once, and attaches what comes back.
// A line voice could not speak keeps its words and gets no audio; the first
// failure of a lap is logged, once, at a level that says whether the operator
// should care — voice not installed is not a problem, voice refusing is.
func (e *Engineer) speakLines(ctx context.Context, cfg config, cues []cueLine) {
	host := e.askable()
	if host == nil || !cfg.speak || len(cues) == 0 {
		return
	}
	if len(cues) > audioLines {
		cues = cues[:audioLines]
	}

	var wg sync.WaitGroup
	errs := make([]error, len(cues))
	for i := range cues {
		wg.Add(1)
		go func() {
			defer wg.Done()
			askCtx, cancel := context.WithTimeout(ctx, speakEach)
			defer cancel()
			payload, err := json.Marshal(struct {
				Text     string `json:"text"`
				Language string `json:"language"`
			}{cues[i].Line, cfg.language})
			if err != nil {
				errs[i] = err
				return
			}
			raw, err := host.Ask(askCtx, kindSpeak, payload)
			if err != nil {
				errs[i] = err
				return
			}
			var s spoken
			if err := json.Unmarshal(raw, &s); err != nil || len(s.Audio) == 0 {
				errs[i] = errors.New("voice answered with no audio")
				return
			}
			cues[i].Audio, cues[i].AudioType = s.Audio, s.ContentType
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err == nil {
			continue
		}
		switch {
		case errors.Is(err, plugin.ErrUnavailable):
			e.Log.LogAttrs(ctx, slog.LevelDebug, "no voice plugin is running, so the lines go as text")
		case errors.Is(err, plugin.ErrNotConfigured):
			e.Log.LogAttrs(ctx, slog.LevelInfo, "the voice plugin is not configured, so the lines go as text")
		case errors.Is(err, plugin.ErrNotAllowed):
			e.Log.LogAttrs(ctx, slog.LevelError, "this plugin's manifest does not let it ask voice", slog.String("reason", err.Error()))
		default:
			e.Log.LogAttrs(ctx, slog.LevelWarn, "a line could not be spoken, so it goes as text", slog.String("reason", err.Error()))
		}
		return
	}
}
