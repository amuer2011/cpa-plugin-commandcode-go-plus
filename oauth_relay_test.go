package plugin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func newRelayTest(t *testing.T) (*CommandCodeGoPlugin, pluginapi.AuthLoginStartResponse) {
	t.Helper()
	_, p := Build(nil)
	start, err := p.StartLogin(context.Background(), pluginapi.AuthLoginStartRequest{BaseURL: "http://127.0.0.1:8317/v0/management/oauth-callback"})
	if err != nil {
		t.Fatal(err)
	}
	return p, start
}

func relayRequest(p *CommandCodeGoPlugin, start pluginapi.AuthLoginStartResponse, action, raw string) pluginapi.ManagementResponse {
	headers := http.Header{"X-Oauth-State": {start.State}, "X-Oauth-Ticket": {p.logins[start.State].ticket}}
	if raw != "" {
		headers.Set("X-OAuth-Callback", base64.RawURLEncoding.EncodeToString([]byte(raw)))
	}
	resp, _ := p.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: relayBase + "/" + action, Headers: headers})
	return resp
}

func TestRelayBootstrapUsesBrowserOriginAndSessionTicket(t *testing.T) {
	p, start := newRelayTest(t)
	if !strings.HasPrefix(start.URL, relayBase+"/start?state=") {
		t.Fatal("login should begin on CPA")
	}
	u, _ := url.Parse(start.URL)
	resp, _ := p.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: u.Path, Query: u.Query()})
	if resp.StatusCode != 200 || !strings.Contains(string(resp.Body), "location.origin") || !strings.Contains(string(resp.Body), "8765/connect#") {
		t.Fatal("missing automatic bootstrap")
	}
	if strings.Contains(string(resp.Body), "127.0.0.1:8317") || strings.Contains(string(resp.Body), "management-key") {
		t.Fatal("internal addresses or admin keys leaked")
	}
	if p.logins[start.State].ticket == start.State {
		t.Fatal("relay ticket must be independent of state")
	}
	if resp.Headers.Get("Cache-Control") != "no-store" {
		t.Fatal("bootstrap must not be cached")
	}
}

func TestRelayCallbackConsumedOnceByAuthenticatedHostPoll(t *testing.T) {
	p, start := newRelayTest(t)
	raw, _ := json.Marshal(map[string]string{"state": start.State, "code": `{"apiKey":"test-secret","userId":"u1","userName":"user","keyName":"main"}`})
	if got := relayRequest(p, start, "submit", string(raw)); got.StatusCode != 200 {
		t.Fatal(string(got.Body))
	}
	// A duplicate cannot replace the first callback or enqueue another one.
	if got := relayRequest(p, start, "submit", "invalid"); got.StatusCode != 200 {
		t.Fatal("duplicate should acknowledge receipt")
	}
	got, err := p.PollLogin(context.Background(), pluginapi.AuthLoginPollRequest{State: start.State, Host: pluginapi.HostConfigSummary{AuthDir: t.TempDir()}, HTTPClient: fakeLoginHTTP{status: 200}})
	if err != nil || got.Status != pluginapi.AuthLoginStatusSuccess || got.Auth.FileName != "user@example.test-commandcode-go.json" {
		t.Fatalf("callback not validated: %+v %v", got, err)
	}
	if p.claimRelay(start.State) != nil {
		t.Fatal("callback must only be consumed once")
	}
	if p.logins[start.State].status != "validated" {
		t.Fatal("validation status missing")
	}
	if got := relayRequest(p, start, "check", ""); strings.Contains(string(got.Body), "test-secret") {
		t.Fatal("status leaked key")
	}
}

func TestRelayRejectsBadTicketStateExpiredAndIncompleteCallbacks(t *testing.T) {
	p, start := newRelayTest(t)
	resp, _ := p.HandleManagement(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: relayBase + "/submit", Headers: http.Header{"X-Oauth-State": {start.State}, "X-Oauth-Ticket": {"bad"}}})
	if resp.StatusCode != 403 {
		t.Fatal("invalid ticket accepted")
	}
	for _, raw := range []string{`{"state":"wrong","code":"{}"}`, `{"state":"` + start.State + `","code":"{}"}`} {
		if resp := relayRequest(p, start, "submit", raw); resp.StatusCode != 400 {
			t.Fatal("invalid callback accepted")
		}
	}
	if p.logins[start.State].submitted {
		t.Fatal("invalid request consumed ticket")
	}
	p.logins[start.State].expires = time.Now().Add(-time.Second)
	if resp := relayRequest(p, start, "check", ""); resp.StatusCode != 410 {
		t.Fatal("expired session accepted")
	}
}

func TestRelayPublicURLAndRegistration(t *testing.T) {
	_, p := Build([]byte("oauth_public_url: https://cpa.example.test\n"))
	start, err := p.StartLogin(context.Background(), pluginapi.AuthLoginStartRequest{BaseURL: "http://localhost:8317/v0/management/oauth-callback"})
	if err != nil || !strings.HasPrefix(start.URL, "https://cpa.example.test"+relayBase) {
		t.Fatal("public origin ignored")
	}
	registration, _ := p.RegisterManagement(context.Background(), pluginapi.ManagementRegistrationRequest{})
	if len(registration.Resources) != 3 || len(registration.Routes) != 0 {
		t.Fatal("unexpected routes")
	}
	for _, resource := range registration.Resources {
		if resource.Handler == nil {
			t.Fatal("missing handler")
		}
	}
}
