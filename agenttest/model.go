// Package agenttest is a scripted model for testing code that runs the agent
// loop, with no model call: replies come from a function of the request, and
// every request is recorded. It speaks through the Anthropic adapter, so a
// test also exercises the requests that adapter builds.
package agenttest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/nodelogicau/agentloop/agent"
)

// Reply is a scripted assistant turn: text, then tool calls.
type Reply struct {
	Text  string
	Tools []ToolUse
	// In and Out are the input and output tokens reported for the turn;
	// both zero reports 100 and 10.
	In, Out int64
	// Refuse ends the turn with stop_reason "refusal".
	Refuse bool
	// Truncate ends the turn with stop_reason "max_tokens".
	Truncate bool
}

// ToolUse is one scripted tool call.
type ToolUse struct {
	Name  string
	Input map[string]any
}

// Model is an agent.Provider, through the Anthropic adapter, whose replies
// come from Script. It records every request.
type Model struct {
	Script func(p anthropic.MessageNewParams) Reply
	mu     sync.Mutex
	Calls  []anthropic.MessageNewParams
}

// Start implements agent.Provider.
func (m *Model) Start(s agent.Settings, system []string, user string, tools []agent.ToolDef) (agent.Conversation, error) {
	return agent.AnthropicWith(m).Start(s, system, user, tools)
}

// New implements agent.MessagesClient.
func (m *Model) New(_ context.Context, p anthropic.MessageNewParams) (*anthropic.Message, error) {
	m.mu.Lock()
	m.Calls = append(m.Calls, p)
	n := len(m.Calls)
	m.mu.Unlock()
	return Message(m.Script(p), n)
}

// CallCount returns the number of requests made.
func (m *Model) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Calls)
}

// Message builds the n-th API response from a Reply.
func Message(r Reply, n int) (*anthropic.Message, error) {
	var content []map[string]any
	if r.Text != "" {
		content = append(content, map[string]any{"type": "text", "text": r.Text})
	}
	for i, t := range r.Tools {
		in := t.Input
		if in == nil {
			in = map[string]any{}
		}
		content = append(content, map[string]any{"type": "tool_use", "id": fmt.Sprintf("toolu_%d_%d", n, i), "name": t.Name, "input": in})
	}
	stop := "end_turn"
	switch {
	case r.Refuse:
		stop = "refusal"
	case r.Truncate:
		stop = "max_tokens"
	case len(r.Tools) > 0:
		stop = "tool_use"
	}
	in, out := r.In, r.Out
	if in == 0 && out == 0 {
		in, out = 100, 10
	}
	b, err := json.Marshal(map[string]any{
		"id": fmt.Sprintf("msg_%d", n), "type": "message", "role": "assistant", "model": "fake",
		"content": content, "stop_reason": stop,
		"usage": map[string]any{"input_tokens": in, "output_tokens": out},
	})
	if err != nil {
		return nil, err
	}
	var msg anthropic.Message
	return &msg, json.Unmarshal(b, &msg)
}

// ToolNames lists the names of the tools offered in a request.
func ToolNames(p anthropic.MessageNewParams) []string {
	var out []string
	for _, t := range p.Tools {
		if t.OfTool != nil {
			out = append(out, t.OfTool.Name)
		}
	}
	return out
}

// LastUser returns the text of the request's last user message, and whether
// that message carries tool results.
func LastUser(p anthropic.MessageNewParams) (text string, toolResults bool) {
	for i := len(p.Messages) - 1; i >= 0; i-- {
		m := p.Messages[i]
		if m.Role != anthropic.MessageParamRoleUser {
			continue
		}
		var parts []string
		for _, c := range m.Content {
			if c.OfToolResult != nil {
				toolResults = true
			}
			if c.OfText != nil {
				parts = append(parts, c.OfText.Text)
			}
		}
		return strings.Join(parts, "\n"), toolResults
	}
	return "", false
}
