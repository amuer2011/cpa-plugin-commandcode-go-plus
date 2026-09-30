"""Exercise automatic CPA bootstrap and the real vendor form callback."""
import base64
import importlib.util
import json
from pathlib import Path
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import Request, build_opener

spec = importlib.util.spec_from_file_location("bridge", Path(__file__).with_name("commandcode-oauth-bridge.py"))
bridge = importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)
STATE, TICKET = "s" * 43, "t" * 43


class FakeCPA(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        self.server.requests.append((self.path, dict(self.headers)))
        if self.headers.get("X-OAuth-State") not in (STATE, "z" * 43) or self.headers.get("X-OAuth-Ticket") != TICKET:
            self.send_error(403)
            return
        status = self.server.status
        if self.path.endswith("/submit"):
            if self.server.fail_submit:
                self.send_error(503)
                return
            raw = self.headers["X-OAuth-Callback"]
            self.server.callbacks.append(json.loads(base64.urlsafe_b64decode(raw + "=" * (-len(raw) % 4))))
            status = self.server.status = "received"
        self.send_response(200)
        self.end_headers()
        self.wfile.write(json.dumps({"status": status}).encode())


class CallbackTests(unittest.TestCase):
    def setUp(self):
        self.cpa = ThreadingHTTPServer(("127.0.0.1", 0), FakeCPA)
        self.cpa.callbacks, self.cpa.requests = [], []
        self.cpa.status, self.cpa.fail_submit = "wait", False
        self.local = ThreadingHTTPServer(("127.0.0.1", 0), bridge.Handler)
        self.local.session = bridge.Session()
        for server in (self.cpa, self.local):
            threading.Thread(target=server.serve_forever, daemon=True).start()
        self.base = f"http://127.0.0.1:{self.local.server_port}"
        self.opener = build_opener(bridge.NoRedirects())
        self.fields = {"state": STATE, "apiKey": "test-api-key", "userId": "u1", "userName": "user", "keyName": "key"}

    def tearDown(self):
        for server in (self.local, self.cpa):
            server.shutdown()
            server.server_close()

    def bootstrap(self, state=STATE, ticket=TICKET):
        return {"state": state, "ticket": ticket, "cpa": f"http://127.0.0.1:{self.cpa.server_port}",
                "login": "https://commandcode.ai/studio/auth/cli?" + urlencode({"state": state, "callback": bridge.LOCAL_ORIGIN + "/callback", "mode": "redirect"}),
                "csrf": self.local.session.csrf}

    def post(self, path, fields, origin=None, json_body=False, host=None):
        body = json.dumps(fields).encode() if json_body else urlencode(fields).encode()
        headers = {"Content-Type": "application/json" if json_body else "application/x-www-form-urlencoded"}
        if origin is not None:
            headers["Origin"] = origin
        if host:
            headers["Host"] = host
        try:
            with self.opener.open(Request(self.base + path, data=body, headers=headers)) as response:
                return response.status, response.read().decode()
        except HTTPError as error:
            return error.code, error.read().decode()

    def connect(self):
        status, body = self.post("/connect", self.bootstrap(), self.base, True)
        self.assertEqual(status, 200, body)
        return json.loads(body)

    def test_automatic_bootstrap_and_callback_need_no_admin_key(self):
        self.assertEqual(self.connect()["login"], self.bootstrap()["login"])
        status, body = self.post("/callback", self.fields, "https://commandcode.ai")
        self.assertEqual(status, 200, body)
        self.assertNotIn("test-api-key", body)
        self.assertEqual(json.loads(self.cpa.callbacks[0]["code"])["apiKey"], "test-api-key")
        for path, headers in self.cpa.requests:
            self.assertNotIn("?", path)
            self.assertNotIn("Authorization", headers)
            self.assertNotIn("test-api-key", path)
            self.assertTrue(headers["User-Agent"].startswith("Mozilla/5.0"))

    def test_opaque_and_absent_vendor_origin_and_duplicate_callback(self):
        self.connect()
        self.assertEqual(self.post("/callback", self.fields, "null")[0], 200)
        self.assertEqual(self.post("/callback", self.fields)[0], 200)
        self.assertEqual(len(self.cpa.callbacks), 1)

    def test_wrong_state_or_origin_rejected(self):
        self.connect()
        self.assertEqual(self.post("/callback", {**self.fields, "state": "wrong"}, "null")[0], 400)
        self.assertEqual(self.post("/callback", self.fields, "https://evil.test")[0], 400)
        self.assertEqual(self.cpa.callbacks, [])

    def test_connect_requires_local_origin_csrf_and_valid_server_ticket(self):
        self.assertEqual(self.post("/connect", self.bootstrap(), "https://evil.test", True)[0], 400)
        self.assertEqual(self.post("/connect", {**self.bootstrap(), "csrf": "wrong"}, self.base, True)[0], 400)
        self.assertEqual(self.post("/connect", self.bootstrap(ticket="q" * 43), self.base, True)[0], 400)
        self.assertEqual(self.local.session.connections, {})

    def test_expired_connection_returns_to_cpa_without_prompt(self):
        self.connect()
        self.local.session.connections[STATE]["expires"] = time.monotonic() - 1
        status, body = self.post("/callback", self.fields, "null")
        self.assertEqual(status, 400)
        self.assertIn("CPA 管理页", body)
        self.assertNotIn('type="password"', body)
        self.assertEqual(self.cpa.callbacks, [])

    def test_submission_can_retry_after_network_failure(self):
        self.connect()
        self.cpa.fail_submit = True
        self.assertEqual(self.post("/callback", self.fields, "null")[0], 400)
        self.cpa.fail_submit = False
        self.assertEqual(self.local.session.status(STATE), "received")
        self.assertEqual(len(self.cpa.callbacks), 1)
        self.cpa.status = "validated"
        self.assertEqual(self.local.session.status(STATE), "validated")

    def test_multiple_login_sessions(self):
        self.connect()
        self.local.session.connect(self.bootstrap(state="z" * 43))
        self.assertEqual(self.post("/callback", self.fields, "null")[0], 200)
        self.assertEqual(self.post("/callback", {**self.fields, "state": "z" * 43}, "null")[0], 200)
        self.assertEqual({c["state"] for c in self.cpa.callbacks}, {STATE, "z" * 43})

    def test_rebinding_and_insecure_remote_cpa_rejected(self):
        self.assertEqual(self.post("/connect", self.bootstrap(), self.base, True, "evil.test")[0], 400)
        for origin in ("http://remote.test", "https://user:secret@remote.test"):
            with self.assertRaises(bridge.BridgeError):
                bridge.cpa_origin(origin)

    def test_fragment_consumed_and_home_has_no_fields(self):
        with self.opener.open(self.base + "/connect") as response:
            body = response.read().decode()
            self.assertIn("location.hash", body)
            self.assertIn("history.replaceState", body)
            self.assertIn("nonce-", response.headers["Content-Security-Policy"])
        with self.opener.open(self.base) as response:
            self.assertNotIn("<input", response.read().decode())


if __name__ == "__main__":
    unittest.main()
