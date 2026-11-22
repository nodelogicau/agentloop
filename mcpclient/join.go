package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nodelogicau/agentloop/agent"
)

// Sep separates a server's name from a tool's name in a joined tool name,
// as in "particulars__claim_assert".
const Sep = "__"

// MaxToolName is the longest tool name every provider accepts.
const MaxToolName = 64

var serverName = regexp.MustCompile(`^[a-z][a-z0-9]*$`)

// Named is a server given a short name for its tools' prefix.
type Named struct {
	Name   string
	Server *Server
	// Tools, when set, are the tools of this server to offer; nil offers
	// them all.
	Tools []*mcp.Tool
}

// Joined offers the tools of several servers to one loop, each tool named
// "<server>__<tool>", and routes each call to its server. The system prompt
// gets each server's instructions under its name.
type Joined struct {
	servers map[string]*Server
	tools   []agent.ToolDef
	order   []Named
}

// Join combines servers. Names must be lower-case letters and digits,
// starting with a letter, and distinct; every joined tool name must fit
// MaxToolName.
func Join(named ...Named) (*Joined, error) {
	j := &Joined{servers: map[string]*Server{}, order: named}
	for _, n := range named {
		if !serverName.MatchString(n.Name) {
			return nil, fmt.Errorf("server name %q: want lower-case letters and digits, starting with a letter", n.Name)
		}
		if _, dup := j.servers[n.Name]; dup {
			return nil, fmt.Errorf("server name %q given twice", n.Name)
		}
		j.servers[n.Name] = n.Server
		tools := n.Tools
		if tools == nil {
			tools = n.Server.Tools
		}
		for _, t := range tools {
			d, err := ToolDef(t)
			if err != nil {
				return nil, err
			}
			d.Name = n.Name + Sep + t.Name
			if len(d.Name) > MaxToolName {
				return nil, fmt.Errorf("tool %s: longer than %d characters", d.Name, MaxToolName)
			}
			j.tools = append(j.tools, d)
		}
	}
	return j, nil
}

// Tools returns the joined tool definitions, in server order.
func (j *Joined) Tools() []agent.ToolDef { return j.tools }

// Instructions returns each server's instructions under a heading naming
// the server and its tool prefix, in server order. A server without
// instructions is left out.
func (j *Joined) Instructions() string {
	var parts []string
	for _, n := range j.order {
		if strings.TrimSpace(n.Server.Instructions) == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("## %s (tools named %s%s*)\n\n%s", n.Name, n.Name, Sep, n.Server.Instructions))
	}
	return strings.Join(parts, "\n\n")
}

// Split returns the server name and tool name of a joined tool name.
func Split(joined string) (server, tool string, ok bool) {
	return strings.Cut(joined, Sep)
}

// Call implements agent.Caller, routing by the tool's prefix. A name with no
// known prefix is an error result, not a protocol error, so the model can
// correct itself.
func (j *Joined) Call(ctx context.Context, name string, args json.RawMessage) (string, bool, error) {
	server, tool, ok := Split(name)
	s := j.servers[server]
	if !ok || s == nil {
		return fmt.Sprintf("unknown tool %q: no server named %q", name, server), true, nil
	}
	return s.Call(ctx, tool, args)
}

// Close closes every server, returning the first error.
func (j *Joined) Close() error {
	var first error
	for _, n := range j.order {
		if err := n.Server.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
