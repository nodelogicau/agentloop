// Package mcpclient connects to an MCP server, started from a command over
// stdio or reached over Streamable HTTP, and forwards an agent's tool calls
// to it. A harness depends on nothing else the server provides.
//
// Extracted from particulars-bench and intentions-bench (internal/impl). What
// differed between them is now a parameter: the placeholder in a command, the
// environment the server starts with, and the client name it is told.
package mcpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Impl is an implementation under test: a label and a command template.
type Impl struct {
	Label   string `json:"label"`
	Command string `json:"command"`
}

// Parse reads "<label>=<command>". When placeholder is not empty, the
// command must contain it.
func Parse(s, placeholder string) (Impl, error) {
	label, cmd, ok := strings.Cut(s, "=")
	label, cmd = strings.TrimSpace(label), strings.TrimSpace(cmd)
	if !ok || label == "" || cmd == "" {
		return Impl{}, fmt.Errorf("--impl %q: want <label>=<command>", s)
	}
	if placeholder != "" && !strings.Contains(cmd, placeholder) {
		return Impl{}, fmt.Errorf("--impl %s: command must contain %s", label, placeholder)
	}
	return Impl{Label: label, Command: cmd}, nil
}

// Expand replaces every placeholder in a command template with value, quoted
// for /bin/sh.
func Expand(template, placeholder, value string) string {
	return strings.ReplaceAll(template, placeholder, ShellQuote(value))
}

// ShellQuote quotes s as one /bin/sh word.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Client is how the harness introduces itself in initialize. A server may
// use the name as the harness of every write the agent makes.
type Client struct {
	Name    string
	Version string
}

// ServerInfo identifies a server, from initialize.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// StartError is a server that failed before initialize and tools/list
// completed.
type StartError struct {
	Command string
	Err     error
	Stderr  string
}

func (e *StartError) Error() string {
	msg := fmt.Sprintf("starting %q: %v", e.Command, e.Err)
	if s := strings.TrimSpace(e.Stderr); s != "" {
		msg += "\nstderr:\n" + s
	}
	return msg
}

// Server is a connected MCP session.
type Server struct {
	Info         ServerInfo
	Instructions string
	Tools        []*mcp.Tool
	session      *mcp.ClientSession
	stderr       *syncBuffer
}

// Command is a server to start over stdio.
type Command struct {
	// Line is run with /bin/sh -c, already expanded.
	Line string
	// Dir is the working directory; empty for the harness's own.
	Dir string
	// Env is added to the harness's environment, as "K=V".
	Env []string
	// TerminateAfter is how long Close waits for the process to exit after
	// closing its stdin; zero means 2 seconds.
	TerminateAfter time.Duration
}

// Start runs a command and completes initialize and tools/list over stdio.
func Start(ctx context.Context, c Command, client Client) (*Server, error) {
	cmd := exec.Command("/bin/sh", "-c", c.Line)
	cmd.Dir = c.Dir
	cmd.Env = append(os.Environ(), c.Env...)
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	wait := c.TerminateAfter
	if wait == 0 {
		wait = 2 * time.Second
	}
	session, err := connect(ctx, client, &mcp.CommandTransport{Command: cmd, TerminateDuration: wait})
	if err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		return nil, &StartError{Command: c.Line, Err: err, Stderr: stderr.String()}
	}
	return open(ctx, session, stderr, c.Line)
}

// Endpoint is a server to reach over Streamable HTTP.
type Endpoint struct {
	// URL is the MCP endpoint, for example https://host/mcp.
	URL string
	// Token, when set, is sent as "Authorization: Bearer <token>".
	Token string
	// HTTPClient is used when set; otherwise http.DefaultClient.
	HTTPClient *http.Client
}

// Connect reaches a server over Streamable HTTP and completes initialize and
// tools/list.
func Connect(ctx context.Context, e Endpoint, client Client) (*Server, error) {
	hc := e.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	if e.Token != "" {
		base := hc.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		hc = &http.Client{Transport: bearer{token: e.Token, base: base}, Timeout: hc.Timeout}
	}
	session, err := connect(ctx, client, &mcp.StreamableClientTransport{Endpoint: e.URL, HTTPClient: hc})
	if err != nil {
		return nil, &StartError{Command: e.URL, Err: err}
	}
	return open(ctx, session, &syncBuffer{}, e.URL)
}

func connect(ctx context.Context, client Client, t mcp.Transport) (*mcp.ClientSession, error) {
	c := mcp.NewClient(&mcp.Implementation{Name: client.Name, Version: client.Version}, nil)
	return c.Connect(ctx, t, nil)
}

func open(ctx context.Context, session *mcp.ClientSession, stderr *syncBuffer, what string) (*Server, error) {
	s := &Server{session: session, stderr: stderr}
	if init := session.InitializeResult(); init != nil {
		s.Instructions = init.Instructions
		if init.ServerInfo != nil {
			s.Info = ServerInfo{Name: init.ServerInfo.Name, Version: init.ServerInfo.Version}
		}
	}
	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			_ = s.Close()
			return nil, &StartError{Command: what, Err: fmt.Errorf("tools/list: %w", err), Stderr: stderr.String()}
		}
		s.Tools = append(s.Tools, t)
	}
	return s, nil
}

type bearer struct {
	token string
	base  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(r)
}

// Result is a tool call's outcome: the text the model receives, and for an
// error result the error code the server gave.
type Result struct {
	Text    string
	IsError bool
	// Code is the error code: structuredContent.error.code, else a leading
	// "<code>: " in the text. Empty when the result is not an error or
	// carries no code.
	Code string
}

// CallTool forwards a tool call. The text is the text content verbatim, and
// any other content as its JSON; with no content, the structured content as
// JSON.
func (s *Server) CallTool(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	var arguments any = map[string]any{}
	if len(args) > 0 {
		arguments = args
	}
	res, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return Result{IsError: true}, err
	}
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
			continue
		}
		b, err := json.Marshal(c)
		if err != nil {
			return Result{IsError: true}, err
		}
		parts = append(parts, string(b))
	}
	if len(parts) == 0 && res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return Result{IsError: true}, err
		}
		parts = append(parts, string(b))
	}
	out := Result{Text: strings.Join(parts, "\n"), IsError: res.IsError}
	if res.IsError {
		out.Code = errorCode(res.StructuredContent, out.Text)
	}
	return out, nil
}

// Call implements agent.Caller.
func (s *Server) Call(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	r, err := s.CallTool(ctx, name, args)
	return r.Text, r.IsError, err
}

var codePrefix = regexp.MustCompile(`^([a-z][a-z_]*): `)

func errorCode(structured any, text string) string {
	b, err := json.Marshal(structured)
	if err == nil {
		var v struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(b, &v) == nil && v.Error.Code != "" {
			return v.Error.Code
		}
	}
	if m := codePrefix.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// Stderr returns the last 64 KiB a started server has written to stderr;
// empty for a server reached over HTTP.
func (s *Server) Stderr() string { return s.stderr.String() }

// Close ends the session, and a started server's process.
func (s *Server) Close() error { return s.session.Close() }

// syncBuffer is a bytes.Buffer safe for the process's stderr goroutine,
// keeping only the last 64 KiB.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if over := b.buf.Len() - 64<<10; over > 0 {
		b.buf.Next(over)
	}
	return n, err
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
