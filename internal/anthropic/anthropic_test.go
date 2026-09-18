package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func tool() Tool {
	return Tool{
		Name:        "cue",
		Description: "One spoken line.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"line": map[string]any{"type": "string"}},
			"required":   []string{"line"},
		},
	}
}

// A server that answers the way the vendor does, and records what it was asked.
func vendor(t *testing.T, status int, body string) (*Client, *[]byte) {
	t.Helper()
	var sent []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, _ = io.ReadAll(r.Body)
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return &Client{Key: "sk-ant-notarealkey", Model: "claude-sonnet-5", BaseURL: srv.URL}, &sent
}

func TestAskReturnsTheToolsInput(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	c, sent := vendor(t, http.StatusOK, `{
		"content": [
			{"type": "text", "text": "thinking out loud"},
			{"type": "tool_use", "name": "cue", "input": {"line": "Brake later into Turn 4."}}
		],
		"usage": {"input_tokens": 412, "output_tokens": 19}
	}`)

	out, use, err := c.Ask(t.Context(), "you are a race engineer", "turn 4, 14 down", tool(), 200)
	r.NoError(err)

	var answer struct {
		Line string `json:"line"`
	}
	r.NoError(json.Unmarshal(out, &answer))
	r.Equal("Brake later into Turn 4.", answer.Line)
	r.EqualValues(412, use.Input)
	r.EqualValues(19, use.Output)

	// The tool is forced. Without that a model answers in prose often enough to
	// matter, and prose is a cue nobody can use.
	var req map[string]any
	r.NoError(json.Unmarshal(*sent, &req))
	r.Equal("claude-sonnet-5", req["model"])
	r.Equal("you are a race engineer", req["system"])
	choice, ok := req["tool_choice"].(map[string]any)
	r.True(ok, "no tool_choice was sent")
	r.Equal("tool", choice["type"])
	r.Equal("cue", choice["name"])
}

func TestAskSendsTheCredentialInTheHeaderAndNotTheBody(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var header, version, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		header = req.Header.Get("x-api-key")
		version = req.Header.Get("anthropic-version")
		raw, _ := io.ReadAll(req.Body)
		body = string(raw)
		_, _ = io.WriteString(w, `{"content":[{"type":"tool_use","name":"cue","input":{"line":"x"}}],"usage":{}}`)
	}))
	defer srv.Close()

	c := Client{Key: "sk-ant-notarealkey", Model: "m", BaseURL: srv.URL}
	_, _, err := c.Ask(t.Context(), "s", "u", tool(), 100)
	r.NoError(err)
	r.Equal("sk-ant-notarealkey", header)
	r.Equal(APIVersion, version)
	r.NotContains(body, "sk-ant-", "the key was sent in the body")
}

func TestAskOnAVendorThatRefuses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		status    int
		body      string
		contains  string
		retryable bool
	}{
		{
			name: "a key that is not a key", status: http.StatusUnauthorized,
			body:     `{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`,
			contains: "invalid x-api-key",
		},
		{
			name: "going too fast", status: http.StatusTooManyRequests,
			body:      `{"error":{"type":"rate_limit_error","message":"rate limit reached"}}`,
			contains:  "rate limit reached",
			retryable: true,
		},
		{
			name: "the vendor is having a day", status: http.StatusServiceUnavailable,
			body:      `{"error":{"type":"overloaded_error","message":"overloaded"}}`,
			contains:  "overloaded",
			retryable: true,
		},
		{
			name: "a refusal that is not even JSON", status: http.StatusBadGateway,
			body:     `<html>502 Bad Gateway</html>`,
			contains: "Bad Gateway",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			c, _ := vendor(t, tc.status, tc.body)
			_, _, err := c.Ask(t.Context(), "s", "u", tool(), 100)
			r.Error(err)
			r.ErrorIs(err, ErrRefused)
			r.Contains(err.Error(), tc.contains)
			r.Equal(tc.retryable, Retryable(err),
				"a caller cannot tell a wrong key from going too fast")
		})
	}
}

// A reply with no tool call still reports what it cost. The tokens were spent
// whether or not the answer was usable, and the operator is paying for them.
func TestAReplyWithNoStructuredAnswerStillReportsWhatItCost(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	c, _ := vendor(t, http.StatusOK, `{
		"content": [{"type": "text", "text": "I would rather write an essay"}],
		"usage": {"input_tokens": 300, "output_tokens": 500}
	}`)

	_, use, err := c.Ask(t.Context(), "s", "u", tool(), 100)
	r.ErrorIs(err, ErrNoToolUse)
	r.EqualValues(300, use.Input)
	r.EqualValues(500, use.Output, "a wasted call was reported as free")
}

// A tool call by another name is not this call's answer.
func TestAReplyCallingADifferentTool(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	c, _ := vendor(t, http.StatusOK,
		`{"content":[{"type":"tool_use","name":"something_else","input":{"line":"x"}}],"usage":{}}`)
	_, _, err := c.Ask(t.Context(), "s", "u", tool(), 100)
	r.ErrorIs(err, ErrNoToolUse)
}

func TestAReplyThatIsNotJSON(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	c, _ := vendor(t, http.StatusOK, `{not json at all`)
	_, _, err := c.Ask(t.Context(), "s", "u", tool(), 100)
	r.Error(err)
	r.NotErrorIs(err, ErrRefused, "a malformed reply is not the vendor refusing")
}

// The deadline is the caller's. A cue has two seconds and a debrief has thirty,
// and the difference is what a job sizes its own ask from.

// A caller that gave up is not a call that carries on.
func TestAskStopsWhenTheCallerDoes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_, _ = io.WriteString(w, `{"content":[],"usage":{}}`)
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	c := Client{Key: "k", Model: "m", BaseURL: srv.URL}
	_, _, err := c.Ask(ctx, "s", "u", tool(), 100)
	r.Error(err)
	r.ErrorIs(err, context.DeadlineExceeded)
}

// A vendor that cannot be reached, and an address that is not one, are errors
// of this package's own rather than refusals: nothing was refused because
// nothing was asked.
func TestAVendorThatCannotBeReached(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	_, _, err := Client{Key: "k", Model: "m", BaseURL: "http://127.0.0.1:1"}.Ask(t.Context(), "s", "u", tool(), 100)
	r.Error(err)
	r.NotErrorIs(err, ErrRefused)
	r.False(Retryable(err))

	_, _, err = Client{Key: "k", Model: "m", BaseURL: "://not-an-address"}.Ask(t.Context(), "s", "u", tool(), 100)
	r.Error(err)
	r.ErrorContains(err, "could not be built")
}
