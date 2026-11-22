package mcpclient

import (
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nodelogicau/agentloop/agent"
)

// ToolDef returns an MCP tool's name, description, and input schema
// unchanged, for a provider to render.
func ToolDef(t *mcp.Tool) (agent.ToolDef, error) {
	schema, err := SchemaMap(t)
	if err != nil {
		return agent.ToolDef{}, err
	}
	return agent.ToolDef{Name: t.Name, Description: t.Description, Schema: schema}, nil
}

// SchemaMap returns a tool's input schema as decoded JSON, or an empty
// object schema when it has none.
func SchemaMap(t *mcp.Tool) (map[string]any, error) {
	b, err := json.Marshal(t.InputSchema)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("tool %s: input schema: %w", t.Name, err)
	}
	if m == nil {
		m = map[string]any{"type": "object"}
	}
	return m, nil
}

// ToolDefs returns every tool's definition.
func ToolDefs(tools []*mcp.Tool) ([]agent.ToolDef, error) {
	out := make([]agent.ToolDef, 0, len(tools))
	for _, t := range tools {
		d, err := ToolDef(t)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

// ReadOnly reports whether a tool is annotated readOnlyHint: true.
func ReadOnly(t *mcp.Tool) bool { return t.Annotations != nil && t.Annotations.ReadOnlyHint }
