# agentloop

A model's tool-use loop over MCP servers, for the benchmarks of
[particulars](https://github.com/nodelogicau/particulars),
[intentions](https://github.com/nodelogicau/intentions) and
[episodes](https://github.com/nodelogicau/episodes), and for the executive's
benchmark.

Each of these benchmarks measures a system: an agent, taught only by what an
MCP server says, using only the tools that server offers. This module is that
agent. It sends the server's instructions and tools to the model unchanged,
forwards every call, and counts what happened.

It started as a copy in each bench: particulars-bench's `internal/agent` and
`internal/impl`, copied into intentions-bench and then changed on both sides.
This module brings the two copies back together, with the fixes from each.

## Packages

| Package | What it does |
|---|---|
| `agent` | The loop (`Run`, `Session`, `Complete`) and its providers: the Anthropic Messages API and any OpenAI-compatible Chat Completions endpoint |
| `mcpclient` | Starts a server from a command over stdio (`Start`) or reaches one over Streamable HTTP (`Connect`), and forwards calls, keeping error codes. `Join` offers several servers' tools to one loop. |
| `agenttest` | A scripted model for tests: replies from a function of the request, every request recorded, no model call |

## The loop

```go
srv, err := mcpclient.Start(ctx, mcpclient.Command{
    Line: mcpclient.Expand(im.Command, "{workspace}", ws),
    Dir:  ws,
    Env:  []string{"INTENTIONS_NOW=" + now.Format(time.RFC3339)},
}, mcpclient.Client{Name: "intentions-bench", Version: version})

tools, err := mcpclient.ToolDefs(srv.Tools)
p, err := agent.New(settings, os.Getenv("ANTHROPIC_API_KEY"))
res, err := agent.Run(ctx, p, srv, agent.Request{
    Settings: settings, Instructions: srv.Instructions, Phase: phase, User: msg, Tools: tools,
})
```

What `Run` guarantees, whichever provider is used:

- **Instructions come first.** The server's instructions are the first part
  of the system prompt, followed by the phase prompt.
- **Tools are sent as served.** Names, descriptions and schemas are unchanged.
- **Bad calls go back as tool errors and never reach the server.** That
  covers a call of a tool that was not offered, and arguments that are not
  valid JSON. Both count as calls and as tool errors, and the first three
  errors of each tool are kept with their arguments
  (`Result.ToolErrorSamples`).
- **Every tool call is answered**, even on the turn where the cap stops the
  loop. A later message in a `Session` therefore continues a well-formed
  history.
- **Truncation is reported.** `Capped` is set when the turn cap ends the
  loop, or when the provider reports output cut at its limit.
- **A refusal ends the loop.** It returns a `*RefusalError` with the turn's
  usage.
- **Usage is counted per call**, including cache reads and writes, and the
  upstream that served each call when a router names it.

`Session` holds one conversation across several user messages. Each `Send`
runs the loop for one message, and its `Result` counts that message alone.

## Providers

- `anthropic` (default): the system prompt and tools are marked for prompt
  caching, and with tools, so is the conversation so far. `Effort` is sent
  as `output_config.effort`.
- `openai`: any Chat Completions endpoint at `BaseURL` (default
  `https://api.openai.com/v1`). Tools go as functions with strict mode off.
  `Effort` is sent as `reasoning_effort` only when set. `MaxTokens` is sent
  as `max_completion_tokens` only when positive.

`ExtraBody` is merged into every request unchanged, for router options such
as pinning an upstream.

## Several servers in one loop

```go
j, err := mcpclient.Join(
    mcpclient.Named{Name: "particulars", Server: p},
    mcpclient.Named{Name: "intentions", Server: i},
    mcpclient.Named{Name: "episodes", Server: e, Tools: readOnly},
)
res, err := agent.Run(ctx, provider, j, agent.Request{
    Settings: s, Instructions: j.Instructions(), User: msg, Tools: j.Tools(),
})
```

- **Tool names are prefixed** with the server's name: `particulars__claim_assert`.
- **`Instructions` puts each server's instructions under a heading** that
  names its prefix.
- **A call with an unknown prefix is a tool error** that the model can
  correct, not a protocol error.
- **Names are checked.** Server names must be lower-case letters and digits,
  and every joined name must fit the 64-character limit that providers
  share.

## Comparability

Benchmarks compare results only within one report. A change to this module
can change how an agent behaves, so:

- **Pin a version** in each bench's `go.mod`.
- **Record that version** in each run record, from `runtime/debug.ReadBuildInfo`.
- **Treat an upgrade in the middle of a study as a new configuration.**

## Development

`make test` runs every test with the race detector and calls no model.
`make lint` runs `go vet` and golangci-lint. The `mcpclient` tests start the
test binary itself as a fake MCP server, so nothing needs to be installed.

## Licence

MIT. See [LICENSE](LICENSE).
