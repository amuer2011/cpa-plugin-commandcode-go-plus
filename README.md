# cpa-plugin-commandcode-go

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) provider plugin for
[Command Code](https://commandcode.ai) **Go plan** subscriptions ($1/mo).

The Go plan has no Provider API access: `POST /provider/v1/chat/completions`
answers `403 upgrade_required`. The only inference path the plan grants is the
official CLI's own wire — `POST /alpha/generate` — which speaks an agent-style
JSONL protocol instead of OpenAI chat. This plugin implements that protocol, so
Go-plan subscriptions work through CLIProxyAPI like any other provider.

> Verified live on 2026-09-12 against command-code@1.53.1 and independently
> cross-checked with [Mars-Sea/dsh-commandcode-provider](https://github.com/Mars-Sea/dsh-commandcode-provider).
> Unofficial community integration — you need your own Command Code account and
> its terms apply.

## How it works

```
client (openai/claude/responses)
   │  host translates everything to OpenAI chat-completions
   ▼
executor (this plugin)
   │  converts to /alpha/generate wire:
   │  { config, memory, taste, skills,
   │    params: { model, messages, tools, system, max_tokens,
   │              temperature, stream, reasoning_effort? }, threadId }
   ▼
POST https://api.commandcode.ai/alpha/generate
   │  replies JSONL events:
   │  start | start-step | text-delta | reasoning-start/delta/end
   │  tool-call | finish | error
   ▼
executor converts events back to OpenAI chat chunks (bare JSON — the host
adds the "data:" framing and owns stream termination)
```

Details that matter (all reverse-engineered and required for correctness):

- **Reasoning round-trip**: rebuilt assistant turns carry their thinking as a
  `{type:"reasoning"}` block; DeepSeek thinking-mode tool loops are rejected
  without it. Streamed reasoning is emitted as `reasoning_content` deltas and
  rendered as thinking blocks on `/v1/messages`.
- **Tool-call id remap**: the gateway rejects ids longer than 64 chars
  (`input[N].call_id <= 64`); overlong ids from cross-provider histories are
  remapped to short per-request aliases, applied to the call and its result
  alike so the pair stays correlated.
- **Unpaired tool results are dropped** (a result with no matching call cannot
  be correlated by the gateway).
- The gateway gates on CLI-flavoured identity headers (`User-Agent: cli`,
  `x-command-code-version`, `x-cli-environment`), which the executor sets.

## Install

1. Download `commandcode-go-v1.0.0.so` from
   [Releases](https://github.com/sperictao/cpa-plugin-commandcode-go/releases)
   (linux/amd64, built against glibc — Debian 12 base, never alpine/musl).
2. Drop it into the directory mounted at `/CLIProxyAPI/plugins` in your
   CLIProxyAPI container.
3. Add the plugin section to `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    commandcode-go:
      enabled: true
      priority: 100
      api_keys:
        - "user_YOUR_COMMANDCODE_KEY"   # ~/.commandcode/auth.json after `cmd login`
```

4. Restart CLIProxyAPI and check the log for
   `pluginhost: plugin registered plugin_id=commandcode-go`.

Model names are `commandcode-go/<alias>`; the bare alias and the upstream
catalog id (`deepseek/deepseek-v4.1-flash`) route to the same model.

## Models

Defaults (each verified live with HTTP 200 against a Go-plan key):

| alias | upstream id |
|---|---|
| deepseek-v4.1-flash | deepseek/deepseek-v4.1-flash |
| deepseek-v4-flash | deepseek/deepseek-v4-flash |
| deepseek-v4-flash-fast | deepseek/deepseek-v4-flash-fast |
| deepseek-v4-flash-vision-exp | deepseek/deepseek-v4-flash-vision-exp |
| glm-5.3-flash | z-ai/glm-5.3-flash |
| glm-5.3 | zai-org/GLM-5.3 |
| kimi-k3 | moonshotai/Kimi-K3 |
| kimi-k2.7-code | moonshotai/Kimi-K2.7-Code |
| kimi-k2.6 | moonshotai/Kimi-K2.6 |

Override or extend without code changes:

```yaml
    commandcode-go:
      models:
        - alias: deepseek-v4.1-flash
          name: deepseek/deepseek-v4.1-flash
        - bare/upstream/id        # bare strings claim the short name
      base_url: https://api.commandcode.ai   # optional override
```

## Known limitations

- **No image input**: the generate wire carries images only through the
  vendor's durable attachment service, which an external proxy cannot
  fabricate. `image_url` parts degrade to a text marker.
- **Single key**: one Go-plan account = one key; no pooling or failover.
- **One completion per request**: the wire is the CLI's single-step inference
  call — no server-side tool execution or agentic looping.

## Build

Requires Go ≥ 1.26 and a glibc toolchain:

```sh
./build.sh                     # vet + test + build → commandcode-go-v1.0.0.so
# or, on a non-glibc host:
docker run --rm -v "$PWD:/work" -w /work -e CGO_ENABLED=1 golang:bookworm \
  bash -c "apt-get update -qq && ./build.sh"
```

## License

MIT
