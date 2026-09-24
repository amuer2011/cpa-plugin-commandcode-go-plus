package plugin

import (
	"context"
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeLoginHTTP struct {
	status int
	body   string
}

func (f fakeLoginHTTP) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	if req.URL != "https://api.commandcode.ai/alpha/whoami" {
		panic("unexpected URL")
	}
	if req.Headers.Get("Authorization") == "" {
		panic("missing auth header")
	}
	body := f.body
	if body == "" {
		body = `{"data":{"user":{"email":"user@example.test"}}}`
	}
	return pluginapi.HTTPResponse{StatusCode: f.status, Body: []byte(body)}, nil
}
func (f fakeLoginHTTP) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	panic("unexpected stream")
}
func TestStartLoginBuildsSafeCallbackURL(t *testing.T) {
	got, err := (&CommandCodeGoPlugin{}).StartLogin(context.Background(), pluginapi.AuthLoginStartRequest{BaseURL: "https://cpa.example.test/v0/management/oauth-callback"})
	if err != nil {
		t.Fatal(err)
	}
	if got.State == "" || len(got.State) < 40 || time.Until(got.ExpiresAt) <= 0 {
		t.Fatal("invalid state or expiry")
	}
	login, err := url.Parse(got.URL)
	if err != nil {
		t.Fatal(err)
	}
	if login.Host != "commandcode.ai" || login.Path != "/studio/auth/cli" {
		t.Fatal("unexpected login endpoint")
	}
	callback, err := url.Parse(login.Query().Get("callback"))
	if err != nil {
		t.Fatal(err)
	}
	if callback.Scheme != "http" || callback.Host != "127.0.0.1:8765" || callback.Path != "/callback" || callback.RawQuery != "" {
		t.Fatal("invalid callback parameters")
	}
	if login.Query().Get("state") != got.State || login.Query().Get("mode") != "redirect" {
		t.Fatal("invalid login parameters")
	}
}
func TestPollLoginConsumesAndValidatesCallback(t *testing.T) {
	dir := t.TempDir()
	state := "abcdefghijklmnopqrstuvwxyz0123456789ABCDEF"
	callback := map[string]any{"code": `{"apiKey":"not-a-real-secret","userId":"u-1","userName":"user-handle","keyName":"main"}`, "state": state}
	raw, _ := json.Marshal(callback)
	path := filepath.Join(dir, ".oauth-"+Provider+"-"+state+".oauth")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := (&CommandCodeGoPlugin{}).PollLogin(context.Background(), pluginapi.AuthLoginPollRequest{Provider: Provider, State: state, Host: pluginapi.HostConfigSummary{AuthDir: dir}, HTTPClient: fakeLoginHTTP{status: 200}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != pluginapi.AuthLoginStatusSuccess || got.Auth.Provider != Provider || got.Auth.FileName != "user@example.test-commandcode-go.json" {
		t.Fatal("login did not succeed")
	}
	var saved map[string]any
	if err := json.Unmarshal(got.Auth.StorageJSON, &saved); err != nil {
		t.Fatal(err)
	}
	if saved["type"] != Provider || saved["user_id"] != "u-1" || saved["email"] != "user@example.test" {
		t.Fatal("incomplete auth metadata")
	}
	if got.Auth.Attributes["api_key"] != "not-a-real-secret" {
		t.Fatal("missing API key attribute")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("callback not consumed")
	}
}
func TestPollLoginPendingAndInvalidCallback(t *testing.T) {
	dir := t.TempDir()
	state := "abcdefghijklmnopqrstuvwxyz0123456789ABCDEF"
	p := &CommandCodeGoPlugin{}
	pending, err := p.PollLogin(context.Background(), pluginapi.AuthLoginPollRequest{State: state, Host: pluginapi.HostConfigSummary{AuthDir: dir}})
	if err != nil || pending.Status != pluginapi.AuthLoginStatusPending {
		t.Fatal("missing callback should remain pending")
	}
	path := filepath.Join(dir, ".oauth-"+Provider+"-"+state+".oauth")
	_ = os.WriteFile(path, []byte(`{"state":"wrong","code":"{}"}`), 0600)
	failed, err := p.PollLogin(context.Background(), pluginapi.AuthLoginPollRequest{State: state, Host: pluginapi.HostConfigSummary{AuthDir: dir}})
	if err != nil || failed.Status != pluginapi.AuthLoginStatusError {
		t.Fatal("mismatched callback must fail")
	}
	if strings.Contains(failed.Message, "not-a-real-secret") {
		t.Fatal("credential echoed")
	}
}

var _ pluginapi.HostHTTPClient = fakeLoginHTTP{}
var _ = http.MethodGet

func TestCommandCodeAuthFileName(t *testing.T) {
	tests := []struct{ email, want string }{
		{"user+second@example.test", "user+second@example.test-commandcode-go.json"},
		{"../escape@example.test", ""},
		{"user@../example.test", ""},
		{"user/path@example.test", ""},
		{"user\\name@example.test", ""},
		{"user name@example.test", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			got, err := commandCodeAuthFileName(tt.email)
			if got != tt.want || (err != nil) != (tt.want == "") {
				t.Fatalf("commandCodeAuthFileName(%q) = %q, %v; want %q", tt.email, got, err, tt.want)
			}
		})
	}
}

func TestPollLoginEmailSources(t *testing.T) {
	cases := []struct {
		name, userName, callbackEmail, whoami, expected string
	}{
		{"nested_data_user", "handle", "", `{"data":{"user":{"email":"nested@example.test"}}}`, "nested@example.test-commandcode-go.json"},
		{"root_user", "handle", "", `{"user":{"email":"root@example.test"}}`, "root@example.test-commandcode-go.json"},
		{"callback_email", "handle", "callback@example.test", `{"ok":true}`, "callback@example.test-commandcode-go.json"},
		{"legacy_email_username", "user@example.test", "", `{"ok":true}`, "user@example.test-commandcode-go.json"},
		{"missing_email", "handle", "", `{"ok":true}`, ""},
		{"unsafe_whoami_email", "handle", "", `{"data":{"user":{"email":"../unsafe@example.test"}}}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			state := "abcdefghijklmnopqrstuvwxyz0123456789ABCDEF"
			code, err := json.Marshal(map[string]any{"apiKey": "fake-key", "userId": "id-1", "userName": tc.userName, "email": tc.callbackEmail, "keyName": "main"})
			if err != nil {
				t.Fatal(err)
			}
			callback, err := json.Marshal(map[string]string{"code": string(code), "state": state})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, ".oauth-"+Provider+"-"+state+".oauth")
			if err := os.WriteFile(path, callback, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := (&CommandCodeGoPlugin{}).PollLogin(context.Background(), pluginapi.AuthLoginPollRequest{State: state, Host: pluginapi.HostConfigSummary{AuthDir: dir}, HTTPClient: fakeLoginHTTP{status: 200, body: tc.whoami}})
			if err != nil {
				t.Fatal(err)
			}
			if tc.expected == "" {
				if got.Status != pluginapi.AuthLoginStatusError {
					t.Fatalf("expected safe error: %v", got.Status)
				}
			} else {
				if got.Status != pluginapi.AuthLoginStatusSuccess || got.Auth.FileName != tc.expected {
					t.Fatalf("unexpected OAuth outcome: %v, %q", got.Status, got.Auth.FileName)
				}
			}
		})
	}
}
