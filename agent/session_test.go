package agent_test

import (
	"context"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/nodelogicau/agentloop/agent"
	"github.com/nodelogicau/agentloop/agenttest"
)

func TestSessionContinuesTheConversation(t *testing.T) {
	m := &agenttest.Model{Script: func(p anthropic.MessageNewParams) agenttest.Reply {
		text, results := agenttest.LastUser(p)
		switch {
		case results:
			return agenttest.Reply{Text: "Noted."}
		case text == "first":
			return agenttest.Reply{Tools: []agenttest.ToolUse{{Name: "knowledge_recall"}}}
		default:
			return agenttest.Reply{Text: "Second answer."}
		}
	}}
	s := agent.NewSession(m, &caller{}, agent.Request{Settings: agent.Settings{Model: "m", MaxTurns: 5}, Instructions: "I", Phase: "P", Tools: tools})
	res, err := s.Send(context.Background(), "first")
	if err != nil || res.Text != "Noted." || res.Turns != 2 {
		t.Fatalf("first: %+v %v", res, err)
	}
	res, err = s.Send(context.Background(), "second")
	if err != nil || res.Text != "Second answer." || res.Turns != 1 {
		t.Fatalf("second: %+v %v", res, err)
	}
	// The second message follows the whole first exchange: user, tool use,
	// tool results, final text, then the new user message.
	last := m.Calls[len(m.Calls)-1]
	if len(last.Messages) != 5 || len(last.System) != 2 {
		t.Errorf("history %d messages, %d system blocks", len(last.Messages), len(last.System))
	}
}

func TestSessionCountsEachMessageApart(t *testing.T) {
	p := &scripted{turns: []agent.Turn{
		{More: true, ToolCalls: []agent.ToolCall{{ID: "1", Name: "fails", Args: []byte(`{}`)}}},
		{Text: "one"},
		{Text: "two"},
	}}
	s := agent.NewSession(p, &caller{}, agent.Request{Settings: agent.Settings{MaxTurns: 5}, Tools: tools})
	if _, err := s.Send(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	res, err := s.Send(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	if res.Turns != 1 || res.ToolErrors != 0 || len(res.ToolCalls) != 0 || res.Usage.Calls != 1 {
		t.Errorf("second message's result carries the first's counts: %+v", res)
	}
	if len(p.users) != 1 || p.users[0] != "b" {
		t.Errorf("follow-up user messages %v", p.users)
	}
}

func TestOpenAISessionAppendsUserMessage(t *testing.T) {
	s := &chatServer{t: t, responses: []any{
		completion("", "stop", map[string]any{"content": "one"}, 10, 0, 1),
		completion("", "stop", map[string]any{"content": "two"}, 10, 0, 1),
	}}
	p, settings := openAI(t, s, agent.Settings{Model: "m", MaxTurns: 3})
	sess := agent.NewSession(p, &caller{}, agent.Request{Settings: settings, Tools: tools})
	if _, err := sess.Send(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Send(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	msgs := s.requests[1]["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("second request has %d messages", len(msgs))
	}
	if m := msgs[2].(map[string]any); m["role"] != "user" || m["content"] != "second" {
		t.Errorf("follow-up message %v", m)
	}
}

func TestCappedTurnAnswersItsToolCalls(t *testing.T) {
	p := &scripted{turns: []agent.Turn{
		{More: true, ToolCalls: []agent.ToolCall{{ID: "1", Name: "knowledge_recall", Args: []byte(`{}`)}}},
	}}
	res, err := agent.Run(context.Background(), p, &caller{}, agent.Request{Settings: agent.Settings{MaxTurns: 1}, User: "Q", Tools: tools})
	if err != nil || !res.Capped {
		t.Fatalf("%+v %v", res, err)
	}
	if len(p.results) != 1 {
		t.Errorf("tool calls of the capped turn left unanswered")
	}
}

func TestAnthropicMaxTokensIsCapped(t *testing.T) {
	m := &agenttest.Model{Script: func(anthropic.MessageNewParams) agenttest.Reply {
		return agenttest.Reply{Text: "partial", Truncate: true}
	}}
	res, err := agent.Run(context.Background(), m, &caller{}, agent.Request{Settings: agent.Settings{Model: "m", MaxTurns: 3}, User: "Q"})
	if err != nil || !res.Capped || res.Text != "partial" || res.StopReason != "max_tokens" {
		t.Errorf("max_tokens: err %v, result %+v", err, res)
	}
}
