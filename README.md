# CommandCode Go Plugin for CLIProxyAPI

## 中文

本插件将 CommandCode Go 订阅接入 CLIProxyAPI（CPA），可通过 CPA 使用模型、管理账号并查看配额。

**使用方法**
1. 从 [GitHub Releases](https://github.com/amuer2011/cpa-plugin-commandcode-go-plus/releases) 下载与 CPA 容器架构匹配的插件（`amd64` 或 `arm64`）。
2. 插件文件格式为 `commandcode-go-v<版本>+linux.debian12.glibc.<架构>.so`，体现系统、Debian 12/glibc 构建基线与架构（不适用于 Alpine/musl）。直接放入 CPA 插件目录并重启 CPA，无需解压或重命名。系统与架构信息位于 `-v` 后，CPA 仍识别插件 ID 为 `commandcode-go`。
3. 在 CPA 配置中启用插件：

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

4. 在 CPA 管理页点击 CommandCode 登录（按钮可能仍位于 OAuth 区域），在 CPA 自己托管的 API Key 输入页粘贴来自自己 CommandCode 账号的有效 API Key 并提交。请保持原 CPA 管理页打开，等待 CPA 校验并保存账号。无需本地服务、Python 或回调助手。
5. 使用模型名 `commandcode-go/<模型名>` 发送请求，例如 `commandcode-go/deepseek-v4.1-flash`。配额可在 CPA 配额页面查看。
<img width="2774" height="956" alt="2" src="https://github.com/user-attachments/assets/1c7e7762-8d67-4308-925d-64e9366e5180" />
<img width="1564" height="1150" alt="1" src="https://github.com/user-attachments/assets/a7ec5985-0314-422a-a461-afeda6c233cf" />

## 模型目录

模型列表不再写死在代码里，默认从 `GET https://api.commandcode.ai/provider/v1/models` 拉取（10 分钟缓存）；插件启动时先用构建期快照 [`assets/models-snapshot.json`](assets/models-snapshot.json) 兜底，该文件可用同一条接口的返回重新生成。

- 模型 ID 为 `commandcode-go/<上游短名>`，例如 `commandcode-go/deepseek-v4.1-flash-fast`；也接受完整上游名 `commandcode-go/deepseek/deepseek-v4.1-flash-fast`，大小写不敏感，`(high)` 这类推理后缀会被忽略。
- **按账号套餐过滤**：`ModelsForAuth` 用该账号的 API Key 读取 `/alpha/whoami`、`/alpha/billing/subscriptions`、`/alpha/billing/credits`（5 分钟缓存），只列出当前套餐能跑的模型。有按量余额（`purchasedCredits + freeCredits > 0`）时不过滤；查不到套餐时也不过滤（fail-open），不会因计费接口抖动把列表清空。
- 套餐档位映射在 [`assets/model-tiers.json`](assets/model-tiers.json)（模型 → 最低套餐），与官方 plan 页面同步；`/provider/v1/models` 本身不带套餐信息，所以这份数据必须随版本更新。
- 网关缺后端或地区受限的模型记在 `assets/model-tiers.json` 的 `unavailable` 里：列表不展示，但仍可显式调用（会拿到上游错误）。2026-10-19 用 Go 账号逐条实测，当前 9 条。
- 可选项：
  - `models:` 显式白名单，写了就覆盖目录（旧的 `alias`/`name` 形式继续可用）；
  - `models_tier: go`（或 `goat`/`pro`/`provider`/`max`/`all`）限制 CPA 全局模型页展示的上限，默认 `all`；
  - `disable_plan_filter: true` 关闭按账号的套餐过滤。

## 缓存用量统计

流式和非流式响应均保留上游 `totalUsage.inputTokenDetails`：

- `cacheReadTokens` → `usage.prompt_tokens_details.cached_tokens`，同时输出 `cache_read_input_tokens`。
- `cacheWriteTokens` → `usage.cache_creation_input_tokens`（扩展字段，不计入缓存命中）。
- `noCacheTokens` → `usage.prompt_cache_miss_tokens`（扩展字段）。

总 token 数沿用上游 `inputTokens` / `outputTokens`，不重复累加缓存明细。上游未返回的明细字段不补造；sub2api 是否展示缓存创建取决于其版本是否读取对应扩展字段。

## 手动 API Key 登录

CPA 管理页中的 CommandCode 登录按钮（可能仍位于 OAuth 区域）会打开由 CPA 自己托管的 API Key 输入页。请从自己的 CommandCode 账号获取有效 API Key，在该页手动粘贴并提交；不能用 CPA 管理密钥代替 CommandCode API Key。

- 输入页通过一次性 state/ticket 绑定当前会话，30 分钟到期，凭证只能提交一次。
- CPA 服务器用提交的 API Key 调用 `https://api.commandcode.ai/alpha/whoami` 校验并取得账号邮箱，随后按 `邮箱-commandcode-go.json` 保存认证文件。
- 请保持原 CPA 管理页打开：认证文件由该页面的登录状态轮询保存。
- 凭据只经同源请求头传输，不进入 URL。
- 全程不需要本地服务、Python 或回调助手，不会自动跳转上游授权，也不会自动获取凭据。

现有 SDK 资源路由只有 GET，插件内部保留 `/v0/resource/plugins/commandcode-go/oauth/` 路径以兼容 nginx 配置；nginx 仍须将该路径反代给 CPA，现有白名单同样适用。默认自动使用 CPA 管理页的域名；若管理前端与后端位于不同域名，可在插件配置中设置 `oauth_public_url: https://你的CPA域名`，指定 CPA 的 HTTPS origin。

## English

This plugin connects a CommandCode Go subscription to CLIProxyAPI (CPA), allowing you to use models, manage accounts, and view quota through CPA.

**Usage**
1. Download the plugin matching your CPA container architecture (`amd64` or `arm64`) from [GitHub Releases](https://github.com/amuer2011/cpa-plugin-commandcode-go-plus/releases).
2. Plugin files use `commandcode-go-v<version>+linux.debian12.glibc.<arch>.so`, identifying the OS, Debian 12/glibc build baseline, and architecture (not compatible with Alpine/musl). Place the file directly in CPA's plugin directory and restart CPA; no extraction or renaming is needed. Platform information follows `-v`, keeping CPA's plugin ID as `commandcode-go`.
3. Enable the plugin in CPA configuration:

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

4. Click CommandCode login on the CPA management page (the button may still sit in the OAuth area), then paste a valid API Key from your own CommandCode account into the CPA-hosted API Key input page and submit it. Keep the original CPA management page open while CPA verifies the key and saves the account. No local service, Python, or callback helper is required.
5. Send requests using `commandcode-go/<model-name>`, for example `commandcode-go/deepseek-v4.1-flash`. View quota on CPA's quota page.

## Model catalog

The model list is no longer hard-coded: it comes from `GET https://api.commandcode.ai/provider/v1/models` (10-minute cache). At startup the plugin falls back to the build-time snapshot in [`assets/models-snapshot.json`](assets/models-snapshot.json), which can be regenerated from the same endpoint.

- Model IDs are `commandcode-go/<upstream short name>`, e.g. `commandcode-go/deepseek-v4.1-flash-fast`. The full upstream name (`commandcode-go/deepseek/deepseek-v4.1-flash-fast`) is accepted too; matching is case-insensitive and drops reasoning suffixes such as `(high)`.
- **Plan filtering**: `ModelsForAuth` reads `/alpha/whoami`, `/alpha/billing/subscriptions` and `/alpha/billing/credits` with the credential's key (5-minute cache) and lists only the models the current plan serves. On-demand credit (`purchasedCredits + freeCredits > 0`) disables filtering, and an unknown plan fails open, so a billing hiccup never blanks the list.
- Plan tiers live in [`assets/model-tiers.json`](assets/model-tiers.json) (model → minimum plan), synced with the official plan pages. `/provider/v1/models` carries no plan data, so this file must ship with each release.
- Models the vendor's backend refuses (missing backend, regional restriction) are listed under `unavailable` in `assets/model-tiers.json`: hidden from the picker but still routable. Probed one by one with a Go key on 2026-10-19; nine entries today.
- Optional knobs: `models:` is an explicit allowlist that overrides the catalog (the old `alias`/`name` form still works); `models_tier: go` (or `goat`/`pro`/`provider`/`max`/`all`) caps the global CPA model page; `disable_plan_filter: true` turns per-account filtering off.

## Manual API Key login

The CommandCode login button on the CPA management page (which may still sit in the OAuth area) opens an API Key input page hosted by CPA itself. Obtain a valid API Key from your own CommandCode account, paste it into that page, and submit. A CPA management key cannot be used in place of a CommandCode API Key.

- The input page is bound to the session through a one-time state/ticket, expires after 30 minutes, and accepts credentials only once.
- The CPA server verifies the submitted API Key against `https://api.commandcode.ai/alpha/whoami` and reads the account email, then saves the auth file as `email-commandcode-go.json`.
- Keep the original CPA management page open: its login status polling saves the auth file.
- Credentials travel only in same-origin request headers and never appear in the URL.
- No local service, Python, or callback helper is required, and there is no automatic redirect to or automatic completion of upstream authorization.

The pinned SDK exposes only GET resource routes, so the plugin keeps the `/v0/resource/plugins/commandcode-go/oauth/` path internally for nginx compatibility; nginx must still reverse-proxy that path to CPA, and the existing whitelist still applies. By default the CPA management page's domain is used automatically; if the management frontend and backend are on different domains, set `oauth_public_url: https://your-cpa-domain` in the plugin config to specify the CPA HTTPS origin.
