# Changelog

## [0.1.0] - 2026-11-22

### Added

- The `agent` package: the tool-use loop and its providers, from
  particulars-bench `internal/agent` at commit `acfd374`. That includes
  unknown-tool and invalid-argument calls returned as tool errors, tool-error
  samples, prompt caching, and the OpenAI-compatible provider.
- `Session`, `Conversation.AddUser`, and answering every tool call before a
  loop ends, from intentions-bench. `AddUser` is now on both providers.
- The Anthropic provider reports a `max_tokens` stop as truncation, so
  `Result.Capped` is set, as the OpenAI provider already did for `length`.
- The `mcpclient` package, from both benches' `internal/impl`.
  - Starting a server over stdio now takes the command, working directory,
    environment and client name as parameters, instead of each bench's
    constants.
  - Error codes on tool results are kept, as intentions-bench did.
  - New: `Connect` over Streamable HTTP with a bearer token, and `Join` for
    several servers in one loop.
- The `agenttest` package: the scripted model, with `LastUser` and
  `Reply.Truncate`.
