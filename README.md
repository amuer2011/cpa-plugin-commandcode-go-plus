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

## Managed credentials and quota

This build adds `AuthProvider` and `QuotaProvider` capabilities and is pinned to
CLIProxyAPI v7.3.15's SDK. The original config-key execution mode remains
available, but the deployed server now uses **managed credential mode**:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    commandcode-go:
      enabled: true
      priority: 100
      auth_files: true
```

Upload a JSON file based on `auth.example.json` in CPA's **Auth files** page.
Use the API key from your own official Command Code CLI authentication. Keep
`type` exactly `commandcode-go`; this is not a Codex OAuth token. Never commit
the populated JSON or share an exported credential file.

In this mode requests use CPA's credential selection, disabled/deleted files
cannot fall back to YAML keys, and multiple auth files can be managed by the
host. Remove `api_keys` from the plugin config after migration. Existing model
names and aliases remain unchanged.

The quota provider queries the official CLI endpoints through the host HTTP
transport: `/alpha/whoami`, `/alpha/billing/credits`, and
`/alpha/billing/subscriptions`. Organization scope is respected. Returned data
includes subscription plan, remaining subscription/purchased/free credits,
5-hour and weekly remaining fractions, and reset timestamps. Credits are
reported as credits, not assumed to be USD. Missing windows are not synthesized;
upstream failures are not displayed as zero balances. Reset is intentionally
unsupported: local actions cannot replenish an upstream subscription.

The companion management frontend adds **CommandCode Go** to quota management
and auth-file quota cards, reusing existing refresh, upload/download, edit,
disable/enable and deletion controls. The backend itself is unmodified.

Server frontend source: `../cpa-management-commandcode`.
Build with `bun install --frozen-lockfile && bun run verify`, then copy
`dist/index.html` to `../cpa/static/management.html`. The deployed compose mounts
that static directory and config sets `remote-management.disable-auto-update-panel:
true` to prevent official HTML updates from overwriting this customization.
Merge upstream UI changes and rebuild deliberately when upgrading.

Build the plugin using `./build.sh` in a matching Linux/glibc Go container;
it emits `dist/commandcode-go-<version>-<arch>.so` (`amd64` or `arm64`).
Do not place multiple versions of the same plugin in the active plugins
directory. Retain a backup before replacing it.

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

1. Open the **Releases** page of the GitHub repository containing this code and
   download the plugin matching the **CPA container architecture**:
   - Linux x86-64: `commandcode-go-<version>-amd64.so`
   - Linux ARM64/aarch64: `commandcode-go-<version>-arm64.so`

   Both variants are built against glibc on Debian 12, never Alpine/musl.
   Download `SHA256SUMS` as well; verify the downloaded file with
   `sha256sum --ignore-missing -c SHA256SUMS`. The host must support the plugin
   ABI from the pinned CLIProxyAPI v7.3.15 SDK; architecture alone is not enough.
2. **Rename the downloaded file before installing it.** The current CPA host
   derives the plugin ID from the filename and recognizes `-v<version>`; the
   release download name is not a directly installable host filename.

   For example, after verifying the checksum:

   ```sh
   # Choose only the variant matching your CPA container.
   cp commandcode-go-1.2.5-arm64.so commandcode-go-v1.2.5.so
   # For amd64, use commandcode-go-1.2.5-amd64.so as the source instead.
   ```

   Put the renamed file into the directory mounted at `/CLIProxyAPI/plugins`.
   Move previous versions outside that active directory before replacing them.
   Do not install both architectures or retain the original download name in
   that directory: `commandcode-go-1.2.5-arm64` would become a different plugin
   ID, instead of using the existing `commandcode-go` configuration.
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
- **Legacy config mode** uses only the first configured key. Managed auth-file mode delegates selection and failover to CPA.
- **One completion per request**: the wire is the CLI's single-step inference
  call — no server-side tool execution or agentic looping.

## Build

Requires Go 1.26+, Linux amd64 or arm64, and a glibc C compiler. The script
runs `go mod verify`, `go vet`, and `go test` before building with CGO and
`-buildmode=c-shared`. It does not update dependency lock files.

```sh
./build.sh
# VERSION supplies the default (currently 1.2.4), architecture is detected:
# dist/commandcode-go-1.2.4-amd64.so or dist/commandcode-go-1.2.4-arm64.so

# Override the version without editing Go source; an optional leading v is removed.
PLUGIN_VERSION=1.2.5 ./build.sh
python3 scripts/check-plugin.py dist/commandcode-go-1.2.5-amd64.so 1.2.5 amd64
```

Use a matching native Linux runner or a Docker installation that supports the
target platform. On a non-Linux or non-glibc host, for example:

```sh
# Set TARGET_ARCH=arm64 for ARM64.
# A different host architecture requires Docker emulation support.
TARGET_ARCH=amd64
docker run --rm --platform "linux/$TARGET_ARCH" \
  -v "$PWD:/work" -w /work -e PLUGIN_VERSION=1.2.5 -e TARGET_ARCH="$TARGET_ARCH" \
  golang:1.26-bookworm bash -c \
  './build.sh && python3 scripts/check-plugin.py "dist/commandcode-go-${PLUGIN_VERSION}-${TARGET_ARCH}.so" "$PLUGIN_VERSION" "$TARGET_ARCH"'
```

`build.sh` runs tests on the target architecture, so setting `GOARCH` alone is
not supported for cross-compilation. `OUTPUT_DIR` can override `dist/`.
The loader check validates ELF architecture, loads the `.so`, and checks the
registered plugin ID and embedded release version without calling CommandCode.
It is a smoke test, not a full CPA integration/OAuth test.

## GitHub 自动构建与发布

工作流：`.github/workflows/release.yml`。

- 推送到 `main` / `master`、提交 PR 或手动运行 Actions：分别构建并测试
  Linux amd64 / arm64，产物保存在 Actions 的 Artifacts 中，不创建 Release。
- 推送版本标签（如 `v1.2.5`）：两个架构都通过测试、编译、动态库加载和版本
  检查后，自动创建 GitHub Release，并上传：

  ```text
  commandcode-go-1.2.5-amd64.so
  commandcode-go-1.2.5-arm64.so
  SHA256SUMS
  ```

- 标签 `v1.2.5-rc.1` 会发布预发布版本，文件名保留 `1.2.5-rc.1`。
- **发布文件名**不带版本前缀 `v`；插件注册信息中的版本与发布版本一致。
  **安装时**应先校验下载文件，再改名为 `commandcode-go-v<版本>.so`。
  当前 CPA 根据文件名中的 `-v` 分隔符识别插件 ID，不能将带架构的下载文件名
  原样放入插件目录，也不要把两个架构同时放进去。此要求也会写入 Release 说明。
- 发布版本以 **Git 标签** 为准；`VERSION` 仅用于普通分支和本地默认构建。
- 两个构建任务使用原生 `ubuntu-24.04` / `ubuntu-24.04-arm` runner，编译统一
  在 `golang:1.26-bookworm` 容器内进行，不直接链接 Ubuntu 的较新 glibc。
- 构建任务仅有仓库读取权限，只有 Release 任务拥有 `contents: write`。
  使用 GitHub 自动提供的 `GITHUB_TOKEN`，无需配置个人 PAT 或 CommandCode 密钥。
- 已存在的 Release 不会被静默覆盖。修订已发布版本时，请使用新版本标签。

### 首次上传

把当前完整源码（包括 `assets/`、`cmd/`、`scripts/`、`go.mod`、`go.sum` 和
隐藏目录 `.github/`）推送到你自己的 GitHub 仓库。现有 checkout 可能保留原作者
的 `origin`，推送前先检查 `git remote -v`，确认目标仓库属于你并有写权限。
不要上传真实认证 JSON、CPA 配置中的密钥、私钥或 `.so` 二进制；二进制由 Actions
生成并作为 Release 附件发布。`.gitignore` 已排除常见本地凭证及构建产物，但
首次上传前仍需人工审查暂存内容。

在 GitHub 仓库的 **Actions** 页面启用工作流；如果组织限制了 Actions 或 token
写权限，需要管理员允许工作流使用的官方 Actions、对应 runner 和 Release 写入。
本工作流面向 GitHub.com 托管 runner。

### 发布新版本

在源码已经提交并推送到自己的仓库后执行，例如：

```sh
# 可选：同步普通分支 / 本地构建的默认版本。
printf '1.2.5\n' > VERSION
git add VERSION
# 将其他本次准备发布的源码改动一并审查、暂存并提交。
git commit -m "chore: release 1.2.5"
git push origin HEAD

# 标签必须指向包含新代码及 release.yml 的提交。
git tag -a v1.2.5 -m "CommandCode Go 1.2.5"
git push origin v1.2.5
```

只修改 `VERSION` 或推送普通代码不会创建 Release，必须推送版本标签。
Actions 构建发布不连接、不重启、也不自动更新你的 CPA 服务器。

## License

MIT
