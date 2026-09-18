// Package anthropic is the smallest client that can ask for structured output.
//
// It is hand-written rather than the vendor's SDK for two reasons. The plugin
// interface module is depended on by plugins nobody here wrote, including closed
// commercial ones, and every dependency this plugin takes is one they inherit
// through the workspace; and what is needed is one endpoint, one message, one
// tool. An SDK would be a large surface for a small ask.
//
// Everything here is shaped by one rule from COACH.md: the model narrates and
// never computes. The facts are already exact when they arrive, so the job is
// language, and the answer comes back through a tool schema rather than as prose
// somebody has to parse.
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultBaseURL is the API. It is a field on the client so tests can point at
// a local server, and for no other reason: an operator has no business pointing
// their driver's coaching at somewhere else.
const DefaultBaseURL = "https://api.anthropic.com"

// APIVersion is the dated contract this client speaks. It is pinned rather than
// tracking whatever is current, because a version change is a behaviour change
// and a coach whose behaviour changed on its own is a coach nobody trusts.
const APIVersion = "2023-06-01"

// maxResponseBytes bounds what is read back. A cue is a sentence and a debrief
// is a few hundred words; anything approaching this is a vendor misbehaving and
// reading it all would be the plugin's problem to have.
const maxResponseBytes = 1 << 20

// ErrRefused is the vendor refusing: a bad key, a quota, a rate limit. It is
// separated from a malformed answer because an operator can act on it and there
// is nothing to be gained from retrying it inside one call.
var ErrRefused = errors.New("anthropic: the request was refused")

// ErrNoToolUse is a reply that carried no structured answer. It is the shape
// the caller has to discard, and it is worth its own error because the fix is a
// change to the prompt or the schema rather than anything at runtime.
var ErrNoToolUse = errors.New("anthropic: the reply carried no structured answer")

// Client calls the messages endpoint.
type Client struct {
	// Key is the operator's own, lent for one call. It is a string here rather
	// than a plugin.Secret because this package must not depend on the plugin
	// interface; the caller unwraps it at the boundary and this never logs it.
	Key string
	// Model is what answers.
	Model string
	// BaseURL overrides [DefaultBaseURL] for tests.
	BaseURL string
	// HTTP is the client to use. A nil one is a default with no timeout of its
	// own, because every call here carries the caller's deadline and a second
	// timeout underneath it would be a different number to explain.
	HTTP *http.Client
}

// Tool is the schema an answer has to fit. The schema is where the length
// limits live, so an over-long cue is a violation rather than a judgement call.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// Ask sends one message and returns what the tool was called with.
//
// system is the persona and the rules; user is the facts. They are separate
// because the persona is the same on every call and the facts never are, which
// is also what makes the system half worth caching at the vendor.
//
// The answer is the tool's raw input, left as JSON for the caller to unmarshal
// into whatever shape that job expects.
func (c Client) Ask(ctx context.Context, system, user string, tool Tool, maxTokens int) (json.RawMessage, Usage, error) {
	body, err := json.Marshal(request{
		Model:     c.Model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  []message{{Role: "user", Content: user}},
		Tools:     []Tool{tool},
		// Forcing the tool is what makes this structured output rather than a
		// suggestion. Without it a model answers in prose roughly one time in
		// twenty, and that one is a cue nobody can use.
		ToolChoice: toolChoice{Type: "tool", Name: tool.Name},
	})
	if err != nil {
		return nil, Usage{}, fmt.Errorf("anthropic: the request could not be encoded: %w", err)
	}

	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(base, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, Usage{}, fmt.Errorf("anthropic: the request could not be built: %w", err)
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", APIVersion)
	req.Header.Set("x-api-key", c.Key)

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, Usage{}, fmt.Errorf("anthropic: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, res.Body); _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return nil, Usage{}, fmt.Errorf("anthropic: the reply could not be read: %w", err)
	}

	if res.StatusCode != http.StatusOK {
		return nil, Usage{}, refusal(res.StatusCode, raw)
	}

	var reply response
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, Usage{}, fmt.Errorf("anthropic: the reply could not be read: %w", err)
	}
	use := Usage{Input: reply.Usage.InputTokens, Output: reply.Usage.OutputTokens}

	for _, block := range reply.Content {
		if block.Type == "tool_use" && block.Name == tool.Name {
			return block.Input, use, nil
		}
	}
	// The tokens are reported even so: they were spent whether or not the
	// answer was usable, and the operator is paying for them either way.
	return nil, use, ErrNoToolUse
}

// Usage is what one call cost, counted the way the vendor counts it.
type Usage struct{ Input, Output int64 }

// refusal turns a non-200 into an error an operator can act on. The vendor's own
// message is included because it names the problem — an expired key, a rate
// limit, a model that does not exist — far better than a status code does.
func refusal(status int, body []byte) error {
	var e errorReply
	if err := json.Unmarshal(body, &e); err == nil && e.Error.Message != "" {
		return fmt.Errorf("%w: %s (%s)", ErrRefused, e.Error.Message, http.StatusText(status))
	}
	return fmt.Errorf("%w: %s", ErrRefused, http.StatusText(status))
}

// Retryable reports whether waiting would plausibly help. Nothing here retries —
// a cue that arrives after the corner is worse than none — but the caller logs
// this so an operator can tell "your key is wrong" from "you are going too
// fast", which are the same status code to a driver and completely different to
// whoever is paying.
func Retryable(err error) bool {
	return errors.Is(err, ErrRefused) &&
		(strings.Contains(err.Error(), "Too Many Requests") ||
			strings.Contains(err.Error(), "Internal Server Error") ||
			strings.Contains(err.Error(), "Service Unavailable") ||
			strings.Contains(err.Error(), "overloaded"))
}

type request struct {
	Model      string     `json:"model"`
	MaxTokens  int        `json:"max_tokens"`
	System     string     `json:"system,omitempty"`
	Messages   []message  `json:"messages"`
	Tools      []Tool     `json:"tools,omitempty"`
	ToolChoice toolChoice `json:"tool_choice,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type toolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

type response struct {
	Content []contentBlock `json:"content"`
	Usage   struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type errorReply struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}
