package mcpclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nodelogicau/agentloop/agent"
	"github.com/nodelogicau/agentloop/agenttest"
	"github.com/nodelogicau/agentloop/mcpclient"
)

const fakeEnv = "AGENTLOOP_FAKE_SERVER"

// TestMain lets the test binary serve as a fake MCP server over stdio.
func TestMain(m *testing.M) {
	if os.Getenv(fakeEnv) == "1" {
		if msg := os.Getenv("FAKE_FAIL"); msg != "" {
			fmt.Fprintln(os.Stderr, "fake server: refusing to start:", msg)
			os.Exit(3)
		}
		if err := fake("fake", "Use the fake tools.").Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			fmt.Fprintln(os.Stderr, "fake server:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type echoIn struct {
	Text string `json:"text"`
}

// fake is a server whose tools report what the harness set up: its client
// name, environment and working directory, and an error with a code.
func fake(name, instructions string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: name, Version: "1.2.3"}, &mcp.ServerOptions{Instructions: instructions})
	text := func(s string) *mcp.CallToolResult {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
	}
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echo text.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}},
		func(_ context.Context, _ *mcp.CallToolRequest, in echoIn) (*mcp.CallToolResult, any, error) {
			return text(name + ":" + in.Text), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "whoami", Description: "Report the client, environment and directory."},
		func(_ context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			client := ""
			if p := req.Session.InitializeParams(); p != nil && p.ClientInfo != nil {
				client = p.ClientInfo.Name
			}
			wd, _ := os.Getwd()
			return text(strings.Join([]string{client, os.Getenv("FAKE_VALUE"), wd}, "|")), nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "refuse", Description: "Fail with a code."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "refused: not yours to do"}}}, nil, nil
		})
	return s
}

func fakeCommand(env ...string) string {
	return strings.Join(append(append([]string{fakeEnv + "=1"}, env...), mcpclient.ShellQuote(os.Args[0])), " ")
}

var client = mcpclient.Client{Name: "agentloop-test", Version: "0.0.1"}

func start(t *testing.T, c mcpclient.Command) *mcpclient.Server {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := mcpclient.Start(ctx, c, client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestParseAndExpand(t *testing.T) {
	im, err := mcpclient.Parse("ref = intentions serve --mcp --workspace {workspace}", "{workspace}")
	if err != nil || im.Label != "ref" || im.Command != "intentions serve --mcp --workspace {workspace}" {
		t.Fatalf("%+v %v", im, err)
	}
	if _, err := mcpclient.Parse("ref=intentions serve", "{workspace}"); err == nil {
		t.Error("command without the placeholder accepted")
	}
	if _, err := mcpclient.Parse("intentions serve", ""); err == nil {
		t.Error("missing label accepted")
	}
	if got := mcpclient.Expand("serve {workspace}", "{workspace}", "/tmp/it's here"); got != `serve '/tmp/it'\''s here'` {
		t.Errorf("expand %s", got)
	}
}

func TestStartOverStdio(t *testing.T) {
	dir := t.TempDir()
	s := start(t, mcpclient.Command{Line: fakeCommand(), Dir: dir, Env: []string{"FAKE_VALUE=v1"}})
	if s.Info.Name != "fake" || s.Info.Version != "1.2.3" || s.Instructions != "Use the fake tools." || len(s.Tools) != 3 {
		t.Fatalf("server %+v, %d tools", s.Info, len(s.Tools))
	}
	out, isErr, err := s.Call(context.Background(), "whoami", nil)
	if err != nil || isErr {
		t.Fatal(out, err)
	}
	parts := strings.Split(out, "|")
	if parts[0] != "agentloop-test" || parts[1] != "v1" {
		t.Errorf("client and environment %q", out)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got, _ := filepath.EvalSymlinks(parts[2]); got != want {
		t.Errorf("working directory %s, want %s", got, want)
	}
}

func TestErrorResultsKeepTheirCode(t *testing.T) {
	s := start(t, mcpclient.Command{Line: fakeCommand()})
	r, err := s.CallTool(context.Background(), "refuse", json.RawMessage(`{}`))
	if err != nil || !r.IsError || r.Code != "refused" || r.Text != "refused: not yours to do" {
		t.Errorf("%+v %v", r, err)
	}
	r, err = s.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hi"}`))
	if err != nil || r.IsError || r.Code != "" || r.Text != "fake:hi" {
		t.Errorf("%+v %v", r, err)
	}
}

func TestStartFailureCarriesStderr(t *testing.T) {
	_, err := mcpclient.Start(context.Background(), mcpclient.Command{Line: fakeCommand("FAKE_FAIL=no-workspace")}, client)
	var se *mcpclient.StartError
	if !errors.As(err, &se) || !strings.Contains(se.Stderr, "no-workspace") {
		t.Errorf("start error %v", err)
	}
}

func TestToolDefsAndReadOnly(t *testing.T) {
	s := start(t, mcpclient.Command{Line: fakeCommand()})
	defs, err := mcpclient.ToolDefs(s.Tools)
	if err != nil || len(defs) != 3 {
		t.Fatal(defs, err)
	}
	for i, d := range defs {
		if d.Schema["type"] != "object" {
			t.Errorf("tool %s schema %v", d.Name, d.Schema)
		}
		if ro := mcpclient.ReadOnly(s.Tools[i]); ro != (d.Name == "echo") {
			t.Errorf("tool %s read-only %t", d.Name, ro)
		}
	}
}

func httpServer(t *testing.T, token string, srv *mcp.Server) string {
	t.Helper()
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

func TestConnectOverHTTPWithToken(t *testing.T) {
	url := httpServer(t, "s3cret", fake("remote", "Remote tools."))
	ctx := context.Background()
	if _, err := mcpclient.Connect(ctx, mcpclient.Endpoint{URL: url}, client); err == nil {
		t.Fatal("connected without the token")
	}
	s, err := mcpclient.Connect(ctx, mcpclient.Endpoint{URL: url, Token: "s3cret"}, client)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if s.Info.Name != "remote" || len(s.Tools) != 3 {
		t.Fatalf("server %+v, %d tools", s.Info, len(s.Tools))
	}
	out, _, err := s.Call(ctx, "whoami", nil)
	if err != nil || !strings.HasPrefix(out, "agentloop-test|") {
		t.Errorf("whoami %q %v", out, err)
	}
}

func TestJoinRoutesByPrefix(t *testing.T) {
	ctx := context.Background()
	local := start(t, mcpclient.Command{Line: fakeCommand()})
	remote, err := mcpclient.Connect(ctx, mcpclient.Endpoint{URL: httpServer(t, "", fake("remote", "Remote tools."))}, client)
	if err != nil {
		t.Fatal(err)
	}
	var echoOnly []*mcp.Tool
	for _, tl := range remote.Tools {
		if tl.Name == "echo" {
			echoOnly = append(echoOnly, tl)
		}
	}
	j, err := mcpclient.Join(
		mcpclient.Named{Name: "particulars", Server: local},
		mcpclient.Named{Name: "episodes", Server: remote, Tools: echoOnly},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	var names []string
	for _, d := range j.Tools() {
		names = append(names, d.Name)
	}
	if strings.Join(names, ",") != "particulars__echo,particulars__refuse,particulars__whoami,episodes__echo" {
		t.Errorf("tools %v", names)
	}
	in := j.Instructions()
	if !strings.Contains(in, "## particulars (tools named particulars__*)\n\nUse the fake tools.") || !strings.Contains(in, "## episodes") {
		t.Errorf("instructions %q", in)
	}
	for name, want := range map[string]string{"particulars__echo": "fake:x", "episodes__echo": "remote:x"} {
		out, isErr, err := j.Call(ctx, name, json.RawMessage(`{"text":"x"}`))
		if err != nil || isErr || out != want {
			t.Errorf("%s: %q %t %v", name, out, isErr, err)
		}
	}
	if out, isErr, err := j.Call(ctx, "intentions__echo", nil); err != nil || !isErr || !strings.Contains(out, "no server named") {
		t.Errorf("unknown prefix: %q %t %v", out, isErr, err)
	}
}

func TestJoinRejectsBadNames(t *testing.T) {
	s := &mcpclient.Server{Tools: []*mcp.Tool{{Name: strings.Repeat("t", 60)}}}
	for _, name := range []string{"Particulars", "a_b", "", "9x"} {
		if _, err := mcpclient.Join(mcpclient.Named{Name: name, Server: &mcpclient.Server{}}); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
	if _, err := mcpclient.Join(mcpclient.Named{Name: "a", Server: &mcpclient.Server{}}, mcpclient.Named{Name: "a", Server: &mcpclient.Server{}}); err == nil {
		t.Error("duplicate name accepted")
	}
	if _, err := mcpclient.Join(mcpclient.Named{Name: "memory", Server: s}); err == nil {
		t.Error("tool name over the limit accepted")
	}
}

// TestAgentOverJoinedServers runs the loop end to end: a scripted model calls
// a joined tool, the call reaches the right server, and the result returns.
func TestAgentOverJoinedServers(t *testing.T) {
	local := start(t, mcpclient.Command{Line: fakeCommand()})
	j, err := mcpclient.Join(mcpclient.Named{Name: "particulars", Server: local})
	if err != nil {
		t.Fatal(err)
	}
	m := &agenttest.Model{Script: func(p anthropic.MessageNewParams) agenttest.Reply {
		if _, results := agenttest.LastUser(p); results {
			return agenttest.Reply{Text: "done"}
		}
		return agenttest.Reply{Tools: []agenttest.ToolUse{{Name: "particulars__echo", Input: map[string]any{"text": "hello"}}}}
	}}
	res, err := agent.Run(context.Background(), m, j, agent.Request{
		Settings: agent.Settings{Model: "m", MaxTurns: 4}, Instructions: j.Instructions(), User: "go", Tools: j.Tools(),
	})
	if err != nil || res.Text != "done" || res.ToolCalls["particulars__echo"] != 1 || res.ToolErrors != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	tr := m.Calls[1].Messages[2].Content[0].OfToolResult
	if tr == nil || tr.Content[0].OfText.Text != "fake:hello" {
		t.Errorf("tool result %+v", tr)
	}
}
