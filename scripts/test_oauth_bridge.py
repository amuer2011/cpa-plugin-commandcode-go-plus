"""Exercise the real browser form callback against a fake CPA management API."""
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


class FakeCPA(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.headers.get("Authorization") != "Bearer test-management-key":
            self.send_error(401)
            return
        if self.path.endswith("commandcode-go-auth-url"):
            result = {"state": "test-state", "url": "https://commandcode.ai/studio/auth/cli?" + urlencode({
                "callback": bridge.LOCAL_ORIGIN + "/callback", "state": "test-state", "mode": "redirect"})}
        else:
            result = {"status": "ok" if self.server.callbacks else "wait"}
        raw = json.dumps(result).encode()
        self.send_response(200)
        self.end_headers()
        self.wfile.write(raw)

    def do_POST(self):
        if self.headers.get("Authorization") != "Bearer test-management-key":
            self.send_error(401)
            return
        self.server.callbacks.append(json.loads(self.rfile.read(int(self.headers["Content-Length"]))))
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b'{"status":"ok"}')


class CallbackTests(unittest.TestCase):
    def setUp(self):
        self.cpa = ThreadingHTTPServer(("127.0.0.1", 0), FakeCPA)
        self.cpa.callbacks = []
        self.local = ThreadingHTTPServer(("127.0.0.1", 0), bridge.Handler)
        self.local.session = bridge.Session()
        self.local.cpa_url = f"http://127.0.0.1:{self.cpa.server_port}"
        for server in (self.cpa, self.local):
            threading.Thread(target=server.serve_forever, daemon=True).start()
        self.base = f"http://127.0.0.1:{self.local.server_port}"
        self.opener = build_opener(bridge.NoRedirects())
        self.fields = {"state": "test-state", "apiKey": "test-api-key", "userId": "u1",
                       "userName": "name + 测试", "keyName": "default", "email": "test@example.com"}

    def tearDown(self):
        for server in (self.local, self.cpa):
            server.shutdown()
            server.server_close()

    def post(self, path, fields, origin):
        headers = {"Content-Type": "application/x-www-form-urlencoded"}
        if origin is not None:
            headers["Origin"] = origin
        request = Request(self.base + path, data=urlencode(fields).encode(), headers=headers)
        try:
            response = self.opener.open(request)
        except HTTPError as response_error:
            response = response_error
        with response:
            return response.status, response.headers, response.read().decode()

    def start(self):
        status, headers, body = self.post("/start", {"csrf": self.local.session.csrf,
            "address": self.local.cpa_url, "key": "test-management-key"}, bridge.LOCAL_ORIGIN)
        self.assertEqual(status, 303, body)
        self.assertTrue(headers["Location"].startswith("https://commandcode.ai/studio/auth/cli?"))

    def test_vendor_form_posts_credentials_and_host_saves_auth(self):
        self.start()
        status, _, body = self.post("/callback", self.fields, "https://commandcode.ai")
        self.assertEqual(status, 200, body)
        self.assertNotIn("test-api-key", body)
        self.assertNotIn("test-management-key", body)
        callback = self.cpa.callbacks[0]
        self.assertEqual(callback["provider"], "commandcode-go")
        self.assertEqual(callback["state"], "test-state")
        expected = dict(self.fields)
        del expected["state"]
        self.assertEqual(json.loads(callback["code"]), expected)
        self.assertEqual(self.local.session.status(), "ok")
        self.assertIsNone(self.local.session.client)
        self.assertEqual(self.local.session.status(), "ok")

    def test_invalid_callbacks_never_reach_cpa(self):
        self.start()
        for fields, origin, expected in (
            (self.fields, "https://other.example", 403),
            ({**self.fields, "state": "wrong-state"}, "https://commandcode.ai", 400),
            ({**self.fields, "apiKey": ""}, "https://commandcode.ai", 400),
        ):
            self.assertEqual(self.post("/callback", fields, origin)[0], expected)
        self.assertEqual(self.cpa.callbacks, [])

    def test_expired_callback_releases_management_key(self):
        self.start()
        self.local.session.expires = time.monotonic() - 1
        self.assertEqual(self.post("/callback", self.fields, "https://commandcode.ai")[0], 400)
        self.assertEqual(self.cpa.callbacks, [])
        self.assertIsNone(self.local.session.client)

    def test_denial_and_duplicate_callbacks(self):
        self.start()
        denial = {"state": "test-state", "error": "access_denied"}
        self.assertEqual(self.post("/callback", denial, "https://commandcode.ai")[0], 200)
        self.assertIn("error", self.cpa.callbacks[0])
        self.assertNotIn("code", self.cpa.callbacks[0])
        self.assertEqual(self.post("/callback", denial, "https://commandcode.ai")[0], 400)
        self.assertEqual(len(self.cpa.callbacks), 1)

    def test_start_requires_local_origin_and_csrf(self):
        self.assertEqual(self.post("/start", {}, "https://commandcode.ai")[0], 403)
        self.assertEqual(self.post("/start", {"csrf": "wrong"}, bridge.LOCAL_ORIGIN)[0], 400)
        self.assertIsNone(self.local.session.client)

    def test_navigation_with_opaque_origin_still_requires_session_tokens(self):
        for origin in ("null", None, "http://localhost:8765"):
            with self.subTest(origin=origin):
                fields = {"csrf": self.local.session.csrf, "address": self.local.cpa_url,
                          "key": "test-management-key"}
                self.assertEqual(self.post("/start", {**fields, "csrf": "wrong"}, origin)[0], 400)
                self.assertEqual(self.post("/start", fields, origin)[0], 303)
        for origin in ("null", None):
            with self.subTest(callback_origin=origin):
                self.assertEqual(self.post("/callback", {**self.fields, "state": "wrong"}, origin)[0], 400)
        self.assertEqual(self.cpa.callbacks, [])
        self.assertEqual(self.post("/callback", self.fields, "null")[0], 200)
        self.assertEqual(len(self.cpa.callbacks), 1)

    def test_page_does_not_suppress_same_origin_form_origin(self):
        with self.opener.open(self.base + "/") as response:
            self.assertEqual(response.headers["Referrer-Policy"], "same-origin")

    def test_rejects_insecure_management_addresses(self):
        for address in ("http://example.com", "https://key@example.com", "https://example.com?a=1"):
            with self.assertRaises(bridge.BridgeError):
                bridge.management_base(address)


if __name__ == "__main__":
    unittest.main()
