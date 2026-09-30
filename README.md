# CommandCode Go Plugin for CLIProxyAPI

## 中文

本插件将 CommandCode Go 订阅接入 CLIProxyAPI（CPA），可通过 CPA 使用模型、管理 OAuth 账号并查看配额。

**使用方法**
1. 从 [GitHub Releases](https://github.com/amuer2011/cpa-plugin-commandcode-go-plus/releases) 下载与 CPA 容器架构匹配的插件（`amd64` 或 `arm64`）。
2. 将文件重命名为 `commandcode-go-v<版本>.so`，放入 CPA 插件目录并重启 CPA。
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

4. 在 CPA 管理页面的认证文件中添加 CommandCode 账号并完成 OAuth 登录。OAuth 登录需要 CPA 管理页面配置 HTTPS 回调地址。
5. 使用模型名 `commandcode-go/<模型名>` 发送请求，例如 `commandcode-go/deepseek-v4.1-flash`。配额可在 CPA 配额页面查看。
<img width="2774" height="956" alt="2" src="https://github.com/user-attachments/assets/1c7e7762-8d67-4308-925d-64e9366e5180" />
<img width="1564" height="1150" alt="1" src="https://github.com/user-attachments/assets/a7ec5985-0314-422a-a461-afeda6c233cf" />

## 缓存用量统计

流式和非流式响应均保留上游 `totalUsage.inputTokenDetails`：

- `cacheReadTokens` → `usage.prompt_tokens_details.cached_tokens`，同时输出 `cache_read_input_tokens`。
- `cacheWriteTokens` → `usage.cache_creation_input_tokens`（扩展字段，不计入缓存命中）。
- `noCacheTokens` → `usage.prompt_cache_miss_tokens`（扩展字段）。

总 token 数沿用上游 `inputTokens` / `outputTokens`，不重复累加缓存明细。上游未返回的明细字段不补造；sub2api 是否展示缓存创建取决于其版本是否读取对应扩展字段。

## English

This plugin connects a CommandCode Go subscription to CLIProxyAPI (CPA), allowing you to use models, manage OAuth accounts, and view quota through CPA.

**Usage**
1. Download the plugin matching your CPA container architecture (`amd64` or `arm64`) from [GitHub Releases](https://github.com/amuer2011/cpa-plugin-commandcode-go-plus/releases).
2. Rename it to `commandcode-go-v<version>.so`, place it in CPA's plugin directory, and restart CPA.
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

4. Add a CommandCode account under Auth Files in CPA's management UI and complete OAuth sign-in. An HTTPS callback URL must be configured for OAuth sign-in.
5. Send requests using `commandcode-go/<model-name>`, for example `commandcode-go/deepseek-v4.1-flash`. View quota on CPA's quota page.
