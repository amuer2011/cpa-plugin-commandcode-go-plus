#!/usr/bin/env python3
"""Receive CommandCode callbacks for a login initiated in CPA. No admin key needed."""
import argparse
import base64
import hmac
import html
import ipaddress
import json
import secrets
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener

LOCAL_ORIGIN = "http://127.0.0.1:8765"
RELAY_PATH = "/v0/resource/plugins/commandcode-go/oauth"
MAX_BODY = 65536
SESSION_TTL = 30 * 60
BROWSER_USER_AGENT = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"


class BridgeError(Exception):
    pass


class NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def cpa_origin(raw):
    parsed = urlsplit(raw)
    try:
        loopback = ipaddress.ip_address(parsed.hostname or "").is_loopback
    except ValueError:
        loopback = parsed.hostname == "localhost"
    if (not parsed.hostname or parsed.username is not None or parsed.password is not None
            or parsed.query or parsed.fragment or parsed.path not in ("", "/")
            or parsed.scheme not in ("http", "https")
            or (parsed.scheme == "http" and not loopback)):
        raise BridgeError("CPA 授权地址无效，请从 CPA 管理页重新发起登录。")
    return f"{parsed.scheme}://{parsed.netloc}"


class CPA:
    def __init__(self, origin, state, ticket):
        self.base = cpa_origin(origin) + RELAY_PATH
        self.state, self.ticket = state, ticket
        self.opener = build_opener(ProxyHandler({}), NoRedirects())

    def call(self, path, payload=None):
        headers = {"X-OAuth-State": self.state, "X-OAuth-Ticket": self.ticket,
                   "Accept": "application/json", "User-Agent": BROWSER_USER_AGENT}
        if payload is not None:
            raw = json.dumps(payload, ensure_ascii=True).encode()
            headers["X-OAuth-Callback"] = base64.urlsafe_b64encode(raw).decode().rstrip("=")
        # The SDK exposes GET-only resource routes. Credentials use headers,
        # so access logs never contain the API key or the temporary ticket.
        req = Request(self.base + path, headers=headers)
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
            if error.code == 403:
                raise BridgeError("CPA 拒绝本次授权回调，请检查 nginx 白名单并从 CPA 重新登录。") from None
            if error.code in (409, 410):
                raise BridgeError("本次授权已过期或完成，请从 CPA 重新发起登录。") from None
            raise BridgeError(f"CPA 返回 HTTP {error.code}，请检查插件版本及服务器日志。") from None
        except (URLError, TimeoutError, OSError, ValueError):
            raise BridgeError("无法连接 CPA，请检查网络及证书；恢复后可重试本次回调。") from None


class Session:
    def __init__(self):
        self.lock = threading.RLock()
        self.csrf = secrets.token_urlsafe(32)
        self.connections = {}

    def connect(self, payload):
        if not isinstance(payload, dict):
            raise BridgeError("授权会话格式不正确。")
        state, ticket, login = (payload.get(k, "") for k in ("state", "ticket", "login"))
        if not all(isinstance(v, str) and 40 <= len(v) <= 128
                   and all(c.isascii() and (c.isalnum() or c in "-_") for c in v)
                   for v in (state, ticket)):
            raise BridgeError("授权会话无效，请更新插件并从 CPA 重新登录。")
        parsed = urlsplit(login)
        query = parse_qs(parsed.query)
        if (parsed.scheme != "https" or parsed.netloc != "commandcode.ai"
                or parsed.path != "/studio/auth/cli" or parsed.fragment
                or query.get("state") != [state]
                or query.get("callback") != [LOCAL_ORIGIN + "/callback"]
                or query.get("mode") != ["redirect"]):
            raise BridgeError("插件返回了不兼容的授权地址。")
        client = CPA(payload.get("cpa", ""), state, ticket)
        result = client.call("/check")
        if result.get("status") != "wait":
            raise BridgeError("本次授权已完成或失效，请从 CPA 重新登录。")
        with self.lock:
            self.connections = {k: v for k, v in self.connections.items() if v["expires"] > time.monotonic()}
            self.connections[state] = {"client": client, "expires": time.monotonic() + SESSION_TTL,
                                       "pending": None, "submitted": False}
        return login

    def connection(self, state):
        item = self.connections.get(state)
        if not item or time.monotonic() >= item["expires"]:
            self.connections.pop(state, None)
            raise BridgeError("本次授权未连接助手或已过期，请保持助手运行，从 CPA 管理页重新登录。")
        return item

    def submit(self, payload):
        with self.lock:
            state = payload.get("state", "")
            item = self.connection(state)
            if item["submitted"]:
                return state
            callback = {"state": state}
            if payload.get("error"):
                callback["error"] = "CommandCode login was rejected"
            else:
                keys = ("apiKey", "userId", "userName", "keyName")
                if not all(isinstance(payload.get(k), str) and payload[k] for k in keys):
                    raise BridgeError("CommandCode 返回的凭据不完整。")
                callback["code"] = json.dumps({k: payload[k] for k in (*keys, "email") if k in payload})
            item["pending"] = callback
            self.retry(state)
            return state

    def retry(self, state):
        with self.lock:
            item = self.connection(state)
            if item["pending"] is not None:
                result = item["client"].call("/submit", item["pending"])
                if result.get("status") != "received":
                    raise BridgeError("CPA 未接收授权回调，请检查服务器日志。")
                item["pending"] = None
                item["submitted"] = True

    def status(self, state):
        with self.lock:
            self.retry(state)
            result = self.connection(state)["client"].call("/check")
            if result.get("status") == "error":
                self.connections.pop(state, None)
                raise BridgeError("CPA 未能验证账号，请查看原 CPA 管理页的登录结果。")
            return result.get("status")


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def page(self, title, content, status=200, script="", refresh=""):
        nonce = secrets.token_urlsafe(20)
        page = ('<!doctype html><html lang="zh-CN"><meta charset="utf-8">'
                '<meta name="viewport" content="width=device-width,initial-scale=1">'
                + ('<meta http-equiv="refresh" content="3;url=' + html.escape(refresh, quote=True) + '">' if refresh else '')
                + '<title>' + html.escape(title) + '</title><style>'
                'body{font:16px system-ui;max-width:560px;margin:8vh auto;padding:24px;line-height:1.7}'
                '</style><h1>' + html.escape(title) + '</h1>' + content
                + ('<script nonce="' + nonce + '">' + script + '</script>' if script else '') + '</html>').encode()
        self.send_response(status)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(page)))
        self.send_header("Cache-Control", "no-store")
        self.send_header("Referrer-Policy", "same-origin")
        self.send_header("X-Frame-Options", "DENY")
        self.send_header("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'nonce-" + nonce + "'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
        self.end_headers()
        self.wfile.write(page)

    def valid_host(self):
        return self.headers.get("Host") in {f"127.0.0.1:{self.server.server_port}", f"localhost:{self.server.server_port}"}

    def do_GET(self):
        if not self.valid_host():
            self.page("请求被拒绝", "<p>本地地址不匹配。</p>", 403)
            return
        parsed = urlsplit(self.path)
        if parsed.path == "/":
            self.page("回调助手已就绪", "<p>请回到 CPA 管理页点击 CommandCode 登录。无需在这里输入地址或管理密钥。</p>")
        elif parsed.path == "/connect":
            script = '''(async()=>{try{const b=JSON.parse(decodeURIComponent(location.hash.slice(1)));history.replaceState(null,"","/connect");b.csrf=CSRF;const r=await fetch("/connect",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(b)});const d=await r.json();if(!r.ok)throw Error(d.error);location.replace(d.login);}catch(e){document.getElementById("message").textContent=e.message;}})();'''.replace("CSRF", json.dumps(self.server.session.csrf))
            self.page("正在接续 CPA 授权", '<p id="message">正在连接本次登录会话，无需输入管理密钥。</p>', script=script)
        elif parsed.path == "/status":
            state = parse_qs(parsed.query).get("state", [""])[0]
            try:
                status = self.server.session.status(state)
                if status == "validated":
                    self.page("授权验证完成", "<p>请回到原 CPA 管理页查看账号保存结果。可以关闭此页面。</p>")
                else:
                    self.page("正在等待 CPA", "<p>授权回调已提交，请保持原 CPA 管理页打开，由 CPA 验证并保存账号。</p>", refresh="/status?state=" + state)
            except BridgeError as error:
                self.page("登录未完成", '<p>' + html.escape(str(error)) + '</p><a href="' + html.escape(self.path, quote=True) + '">重试</a>', 400)
        else:
            self.page("未找到页面", '<a href="/">返回</a>', 404)

    def do_POST(self):
        self.connection.settimeout(30)
        try:
            if not self.valid_host() or self.path not in ("/connect", "/callback"):
                raise BridgeError("本地回调地址不匹配。")
            local = {LOCAL_ORIGIN, f"http://127.0.0.1:{self.server.server_port}", f"http://localhost:{self.server.server_port}"}
            allowed = local if self.path == "/connect" else {"https://commandcode.ai", None, "null"}
            if self.headers.get("Origin") not in allowed:
                raise BridgeError("请求来源不匹配。")
            size = int(self.headers.get("Content-Length", "0"))
            if not 0 < size <= MAX_BODY:
                raise BridgeError("回调数据大小不正确。")
            body = self.rfile.read(size)
            kind = self.headers.get("Content-Type", "").split(";", 1)[0].strip()
            if kind == "application/x-www-form-urlencoded" and self.path == "/callback":
                fields = parse_qs(body.decode(), keep_blank_values=True, max_num_fields=32)
                if any(len(values) != 1 for values in fields.values()):
                    raise BridgeError("回调字段重复。")
                payload = {key: values[0] for key, values in fields.items()}
            elif kind == "application/json":
                payload = json.loads(body)
                if not isinstance(payload, dict) or not all(isinstance(v, str) for v in payload.values()):
                    raise BridgeError("回调格式不正确。")
            else:
                raise BridgeError("不支持的回调格式。")
            if self.path == "/connect":
                if not hmac.compare_digest(payload.get("csrf", ""), self.server.session.csrf):
                    raise BridgeError("本地连接页面已过期。")
                login = self.server.session.connect(payload)
                self.send_json(200, {"login": login})
            else:
                state = self.server.session.submit(payload)
                self.page("正在等待 CPA", "<p>回调已提交，请保持原 CPA 管理页打开，等待账号验证和保存。</p>", refresh="/status?state=" + state)
        except (BridgeError, ValueError, UnicodeError, TypeError) as error:
            message = str(error) if isinstance(error, BridgeError) else "回调数据格式不正确。"
            if self.path == "/connect":
                self.send_json(400, {"error": message})
            else:
                state = payload.get("state", "") if "payload" in locals() else ""
                retry = ('<a href="/status?state=' + html.escape(state, quote=True) + '">重试提交</a>') if state in self.server.session.connections else '<a href="/">返回</a>'
                self.page("登录未完成", '<p>' + html.escape(message) + '</p>' + retry, 400)

    def send_json(self, status, payload):
        raw = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    # Accepted for compatibility; login context now comes automatically from CPA.
    parser.add_argument("--cpa-url", default="", help=argparse.SUPPRESS)
    parser.parse_args()
    try:
        server = ThreadingHTTPServer(("127.0.0.1", 8765), Handler)
    except OSError:
        parser.exit(1, "无法监听 127.0.0.1:8765，请关闭旧助手或占用该端口的程序。\n")
    server.session = Session()
    print("回调助手已就绪，请在 CPA 管理页点击 CommandCode 登录。无需输入管理密钥。", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.session.connections.clear()
        server.server_close()


if __name__ == "__main__":
    main()
