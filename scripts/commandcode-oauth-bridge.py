#!/usr/bin/env python3
"""Receive CommandCode's browser-local callback and submit it to remote CPA.

Uses Python's standard library. Credentials stay in memory and are never logged.
"""
import argparse
import hmac
import html
import ipaddress
import json
import secrets
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


PROVIDER = "commandcode-go"
LOCAL_ORIGIN = "http://127.0.0.1:8765"
MAX_BODY = 65536
# The management site may reject Python's default User-Agent at its edge proxy.
BROWSER_USER_AGENT = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"


class BridgeError(Exception):
    pass


class NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def management_base(raw):
    parsed = urlsplit(raw.strip())
    try:
        loopback = ipaddress.ip_address(parsed.hostname or "").is_loopback
    except ValueError:
        loopback = parsed.hostname == "localhost"
    if (not parsed.hostname or parsed.username is not None
            or parsed.password is not None or parsed.query or parsed.fragment
            or parsed.scheme not in ("http", "https")
            or (parsed.scheme == "http" and not loopback)):
        raise BridgeError("管理地址须为 HTTPS，或本机 HTTP 回环地址。")
    if parsed.path not in ("", "/", "/management.html", "/v0/management", "/v0/management/"):
        raise BridgeError("请填写 CPA 管理地址，例如 https://cpa.example.com。")
    return f"{parsed.scheme}://{parsed.netloc}/v0/management"


class CPA:
    def __init__(self, base, key):
        self.base = management_base(base)
        self.key = key
        # Do not send the management key to redirect destinations or local proxies.
        from urllib.request import ProxyHandler
        self.opener = build_opener(ProxyHandler({}), NoRedirects())

    def call(self, path, payload=None):
        body = None if payload is None else json.dumps(payload).encode()
        req = Request(self.base + path, data=body, headers={
            "Authorization": "Bearer " + self.key,
            "Accept": "application/json",
            "Content-Type": "application/json",
            "User-Agent": BROWSER_USER_AGENT,
        })
        try:
            with self.opener.open(req, timeout=20) as response:
                raw = response.read(MAX_BODY + 1)
            if len(raw) > MAX_BODY:
                raise BridgeError("CPA 响应过大。")
            result = json.loads(raw)
            if not isinstance(result, dict):
                raise BridgeError("CPA 返回的响应格式不正确。")
            return result
        except HTTPError as error:
            if error.code == 401:
                raise BridgeError("CPA 管理密钥不正确。") from None
            if error.code == 403:
                raise BridgeError("CPA 拒绝请求，请检查管理密钥及 nginx 白名单。") from None
            raise BridgeError(f"CPA 返回 HTTP {error.code}，请检查服务器日志。") from None
        except (URLError, TimeoutError, OSError, ValueError):
            raise BridgeError("无法连接 CPA 管理接口，请检查地址、网络及证书。") from None


class Session:
    def __init__(self):
        self.lock = threading.RLock()
        self.csrf = secrets.token_urlsafe(32)
        self.client = None
        self.state = ""
        self.expires = 0
        self.submitted = False
        self.complete = False

    def start(self, address, key):
        if not key.strip():
            raise BridgeError("请输入 CPA 管理密钥。")
        with self.lock:
            client = CPA(address, key.strip())
            result = client.call("/commandcode-go-auth-url")
            state = result.get("state", "")
            login = result.get("url", "")
            parsed = urlsplit(login)
            query = parse_qs(parsed.query)
            if (not isinstance(state, str) or not state
                    or parsed.scheme != "https" or parsed.netloc != "commandcode.ai"
                    or parsed.path != "/studio/auth/cli"
                    or query.get("state") != [state]
                    or query.get("callback") != [LOCAL_ORIGIN + "/callback"]
                    or query.get("mode") != ["redirect"]):
                raise BridgeError("插件返回了不兼容的授权地址，请确认插件版本。")
            self.client, self.state = client, state
            self.expires = time.monotonic() + 600
            self.submitted = False
            self.complete = False
            return login

    def check(self, state):
        if time.monotonic() >= self.expires:
            self.client = None
        if (not self.client or not state or not hmac.compare_digest(self.state, state)
                or time.monotonic() >= self.expires):
            raise BridgeError("授权会话不存在或已过期，请从助手首页重新登录。")

    def submit(self, payload):
        with self.lock:
            state = payload.get("state", "")
            self.check(state)
            if self.submitted:
                raise BridgeError("该授权回调已提交。")
            body = {"provider": PROVIDER, "state": state}
            if payload.get("error"):
                body["error"] = "CommandCode login was rejected"
            else:
                if not all(isinstance(payload.get(k), str) and payload[k]
                           for k in ("apiKey", "userId", "userName", "keyName")):
                    raise BridgeError("CommandCode 返回的凭据不完整。")
                credentials = {k: payload[k] for k in
                               ("apiKey", "userId", "userName", "keyName", "email") if k in payload}
                body["code"] = json.dumps(credentials)
            self.client.call("/oauth-callback", body)
            self.submitted = True

    def status(self):
        with self.lock:
            if self.complete:
                return "ok"
            self.check(self.state)
            result = self.client.call("/get-auth-status?" + urlencode({"state": self.state}))
            status = result.get("status")
            if status == "ok":
                self.client = None  # Release the management key once the host saved auth.
                self.complete = True
                return "ok"
            if status == "error":
                self.client = None
                raise BridgeError("CPA 未能保存账号，请检查 CPA 日志后重新登录。")
            return "wait"


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass  # URLs and form bodies can contain authentication data.

    def page(self, title, content, status=200, refresh=False):
        page = ('<!doctype html><html lang="zh-CN"><meta charset="utf-8">'
                '<meta name="viewport" content="width=device-width,initial-scale=1">'
                + ('<meta http-equiv="refresh" content="2;url=/status">' if refresh else '')
                + '<title>' + html.escape(title) + '</title><style>'
                'body{font:16px system-ui;max-width:560px;margin:8vh auto;padding:24px;line-height:1.7}'
                'input,button{box-sizing:border-box;width:100%;padding:12px;margin:8px 0;font:inherit}'
                '</style><h1>' + html.escape(title) + '</h1>' + content + '</html>').encode()
        self.send_response(status)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(page)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("Referrer-Policy", "same-origin")
        self.send_header("X-Frame-Options", "DENY")
        self.send_header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https://commandcode.ai; frame-ancestors 'none'")
        self.end_headers()
        self.wfile.write(page)

    def do_GET(self):
        if self.path == "/":
            self.page("CommandCode 登录", '<p>输入 CPA 管理地址与管理密钥，继续完成 CommandCode 授权。账号会自动保存到 CPA。</p>'
                      '<p>密钥仅在本机内存中使用，不写入文件或日志。完成后可关闭助手。</p>'
                      '<form method="post" action="/start">'
                      '<input type="hidden" name="csrf" value="' + self.server.session.csrf + '">'
                      '<label>CPA 管理地址<input name="address" type="url" required value="'
                      + html.escape(self.server.cpa_url, quote=True) + '"></label>'
                      '<label>CPA 管理密钥<input name="key" type="password" autocomplete="off" required></label>'
                      '<button>开始授权</button></form>')
        elif self.path == "/status":
            try:
                status = self.server.session.status()
                if status == "ok":
                    self.page("登录成功", '<p>账号已保存到 CPA。回到管理页面刷新认证文件列表即可使用。</p>')
                else:
                    self.page("正在保存账号", '<p>授权已接收，正在等待 CPA 验证并保存账号。</p>', refresh=True)
            except BridgeError as error:
                self.page("登录未完成", '<p>' + html.escape(str(error)) + '</p><a href="/">重新登录</a>', 400)
        else:
            self.page("未找到页面", '<a href="/">返回首页</a>', 404)

    def do_POST(self):
        self.connection.settimeout(30)
        try:
            if self.path not in ("/start", "/callback"):
                raise BridgeError("无效的回调路径。")
            # Browsers can send an opaque or absent Origin on navigational
            # form POSTs. The per-page CSRF token and pending OAuth state below
            # remain mandatory even when Origin cannot identify the sender.
            allowed_origins = ({LOCAL_ORIGIN, "http://localhost:8765"}
                               if self.path == "/start" else {"https://commandcode.ai"})
            if self.headers.get("Origin") not in allowed_origins | {None, "null"}:
                self.page("请求被拒绝", '<p>请求来源不匹配。</p>', 403)
                return
            size = int(self.headers.get("Content-Length", "0"))
            if size <= 0 or size > MAX_BODY:
                raise BridgeError("回调数据大小不正确。")
            body = self.rfile.read(size)
            kind = self.headers.get("Content-Type", "").split(";", 1)[0].strip()
            if kind == "application/x-www-form-urlencoded":
                fields = parse_qs(body.decode(), keep_blank_values=True, max_num_fields=32)
                if any(len(values) != 1 for values in fields.values()):
                    raise BridgeError("回调字段重复。")
                payload = {key: values[0] for key, values in fields.items()}
            elif kind == "application/json" and self.path == "/callback":
                payload = json.loads(body)
                if not isinstance(payload, dict) or not all(isinstance(v, str) for v in payload.values()):
                    raise BridgeError("回调数据格式不正确。")
            else:
                raise BridgeError("不支持的回调数据格式。")
            if self.path == "/start":
                if not hmac.compare_digest(payload.get("csrf", ""), self.server.session.csrf):
                    raise BridgeError("登录页面已过期，请重新打开首页。")
                login = self.server.session.start(payload.get("address", ""), payload.get("key", ""))
                self.send_response(303)
                self.send_header("Location", login)
                self.send_header("Cache-Control", "no-store")
                self.end_headers()
            else:
                self.server.session.submit(payload)
                self.page("正在保存账号", '<p>授权已接收，正在等待 CPA 验证并保存账号。</p>', refresh=True)
        except (BridgeError, ValueError, UnicodeError) as error:
            message = str(error) if isinstance(error, BridgeError) else "回调数据格式不正确。"
            self.page("登录未完成", '<p>' + html.escape(message) + '</p><a href="/">重新登录</a>', 400)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cpa-url", default="", help="CPA management URL (can also be entered in the local page)")
    args = parser.parse_args()
    try:
        server = ThreadingHTTPServer(("127.0.0.1", 8765), Handler)
    except OSError:
        parser.exit(1, "无法监听 127.0.0.1:8765，请关闭占用该端口的程序。\n")
    server.session = Session()
    server.cpa_url = args.cpa_url
    print("回调助手已启动，请打开 " + LOCAL_ORIGIN, flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.session.client = None
        server.server_close()


if __name__ == "__main__":
    main()
