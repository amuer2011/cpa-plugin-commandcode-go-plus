# CommandCode Go Plugin for CLIProxyAPI

## 中文

本插件将 CommandCode Go 订阅接入 CLIProxyAPI（CPA），可通过 CPA 使用模型、管理 OAuth 账号并查看配额。

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

4. 在浏览器所在电脑使用 Python 3 运行 [commandcode-oauth-bridge.py](scripts/commandcode-oauth-bridge.py)（也可从 Release 下载该原文件），然后在 CPA 管理页点击 CommandCode 登录。助手自动接续本次会话并提交授权回调，无需再次输入地址或管理密钥。授权期间保持助手和原 CPA 管理页打开，等待 CPA 保存账号。
5. 使用模型名 `commandcode-go/<模型名>` 发送请求，例如 `commandcode-go/deepseek-v4.1-flash`。配额可在 CPA 配额页面查看。
<img width="2774" height="956" alt="2" src="https://github.com/user-attachments/assets/1c7e7762-8d67-4308-925d-64e9366e5180" />
<img width="1564" height="1150" alt="1" src="https://github.com/user-attachments/assets/a7ec5985-0314-422a-a461-afeda6c233cf" />

## 缓存用量统计

流式和非流式响应均保留上游 `totalUsage.inputTokenDetails`：

- `cacheReadTokens` → `usage.prompt_tokens_details.cached_tokens`，同时输出 `cache_read_input_tokens`。
- `cacheWriteTokens` → `usage.cache_creation_input_tokens`（扩展字段，不计入缓存命中）。
- `noCacheTokens` → `usage.prompt_cache_miss_tokens`（扩展字段）。

总 token 数沿用上游 `inputTokens` / `outputTokens`，不重复累加缓存明细。上游未返回的明细字段不补造；sub2api 是否展示缓存创建取决于其版本是否读取对应扩展字段。

## 远程 CPA 的 OAuth 回调

CommandCode 只允许 localhost 回调，并在 `mode=redirect` 下通过表单 POST 发送凭据。浏览器访问的 `127.0.0.1` 是用户电脑，服务器上的插件无法接收这个请求。仅安装 `.so` 后直接点击 OAuth 登录，若本机没有回调服务，授权完成后就会出现 `ERR_CONNECTION_REFUSED`。

本地助手使用 Python 3 标准库，无需安装依赖：

```bash
python commandcode-oauth-bridge.py
```

保持助手运行，在 CPA 管理页点击 CommandCode 登录即可。流程为：CPA → 自动连接本地助手 → CommandCode 授权 → 助手接收回调 → CPA 验证并保存。原 CPA 管理页需保持打开，因为 CPA 在该页面的登录状态轮询中保存认证文件。

插件为每次登录生成独立的临时回调凭证，绑定 state，30 分钟过期，只能提交一次回调。助手不会读取或接收管理密钥，也没有管理地址或密钥输入框。临时凭证通过 URL fragment 传给本地助手，API key 和回调凭证通过 HTTPS 请求头提交，不进入请求 URL、页面或普通访问日志。CPA 管理接口的认证保持原样。

助手只监听 `127.0.0.1:8765`。nginx 须将 `/v0/resource/plugins/commandcode-go/oauth/` 转发给 CPA；现有白名单也必须允许浏览器所在电脑访问。默认自动使用 CPA 管理页的域名；若管理前端位于不同域名，可在插件配置中设置 `oauth_public_url: https://你的CPA域名`。更新插件时也请更新并重启助手。助手显示“授权验证完成”后，以 CPA 管理页的账号保存结果为准。

## English

This plugin connects a CommandCode Go subscription to CLIProxyAPI (CPA), allowing you to use models, manage OAuth accounts, and view quota through CPA.

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

4. Run [commandcode-oauth-bridge.py](scripts/commandcode-oauth-bridge.py) with Python 3 on the computer running your browser (also available as a raw Release asset). Start login from the CPA management page. The helper automatically connects using a temporary session ticket and never asks for or receives your management key. Keep both the helper and the original CPA page open until CPA verifies and saves the account. Update both the plugin and helper together.
5. Send requests using `commandcode-go/<model-name>`, for example `commandcode-go/deepseek-v4.1-flash`. View quota on CPA's quota page.
