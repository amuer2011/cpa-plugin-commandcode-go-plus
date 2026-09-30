package plugin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const relayBase = "/v0/resource/plugins/" + Provider + "/oauth"

type relayLogin struct {
	ticket, vendorURL string
	expires           time.Time
	payload           []byte
	submitted         bool
	status            string
}

func (p *CommandCodeGoPlugin) startRelay(_ pluginapi.AuthLoginStartRequest, state, vendorURL string) (pluginapi.AuthLoginStartResponse, error) {
	publicURL := ""
	if p.cfg != nil {
		publicURL = strings.TrimRight(p.cfg.OAuthPublicURL, "/")
	}
	if publicURL != "" {
		u, err := url.Parse(publicURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("oauth_public_url must be an HTTPS origin")
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return pluginapi.AuthLoginStartResponse{}, err
	}
	expires := time.Now().Add(30 * time.Minute)
	p.loginMu.Lock()
	defer p.loginMu.Unlock()
	if p.logins == nil {
		p.logins = make(map[string]*relayLogin)
	}
	for key, session := range p.logins {
		if time.Now().After(session.expires) {
			delete(p.logins, key)
		}
	}
	p.logins[state] = &relayLogin{ticket: base64.RawURLEncoding.EncodeToString(raw), vendorURL: vendorURL, expires: expires, status: "wait"}
	return pluginapi.AuthLoginStartResponse{Provider: Provider, State: state, ExpiresAt: expires, URL: publicURL + relayBase + "/start?state=" + url.QueryEscape(state)}, nil
}

func (p *CommandCodeGoPlugin) RegisterManagement(_ context.Context, _ pluginapi.ManagementRegistrationRequest) (pluginapi.ManagementRegistrationResponse, error) {
	return pluginapi.ManagementRegistrationResponse{Resources: []pluginapi.ResourceRoute{
		{Path: "oauth/start", Handler: p},
		{Path: "oauth/check", Handler: p},
		{Path: "oauth/submit", Handler: p},
	}}, nil
}

func relayJSON(status int, value string) pluginapi.ManagementResponse {
	return pluginapi.ManagementResponse{StatusCode: status, Headers: http.Header{
		"Content-Type": {"application/json"}, "Cache-Control": {"no-store"},
		"Referrer-Policy": {"no-referrer"}, "X-Content-Type-Options": {"nosniff"},
	}, Body: []byte(`{"status":"` + value + `"}`)}
}

func (p *CommandCodeGoPlugin) HandleManagement(_ context.Context, req pluginapi.ManagementRequest) (pluginapi.ManagementResponse, error) {
	p.loginMu.Lock()
	defer p.loginMu.Unlock()
	if req.Method != http.MethodGet {
		return relayJSON(405, "error"), nil
	}
	state := req.Headers.Get("X-OAuth-State")
	if req.Path == relayBase+"/start" {
		state = req.Query.Get("state")
	}
	session := p.logins[state]
	if session == nil || time.Now().After(session.expires) {
		delete(p.logins, state)
		return relayJSON(410, "expired"), nil
	}
	if req.Path == relayBase+"/start" {
		if session.submitted {
			return relayJSON(409, "received"), nil
		}
		bootstrap, _ := json.Marshal(map[string]string{"state": state, "ticket": session.ticket, "login": session.vendorURL})
		// The browser knows the public CPA origin even when the host only
		// supplies an internal loopback BaseURL. No management key is transferred.
		body := `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>CommandCode 登录</title><p>正在接续 CPA 授权。请保持原 CPA 管理页打开。</p><p>请先在浏览器所在电脑启动回调助手。<a id="retry">继续授权</a></p><script>const b=` + string(bootstrap) + `;b.cpa=location.origin;const target="http://127.0.0.1:8765/connect#"+encodeURIComponent(JSON.stringify(b));document.getElementById("retry").href=target;location.replace(target);</script></html>`
		return pluginapi.ManagementResponse{StatusCode: 200, Headers: http.Header{
			"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-store"},
			"Referrer-Policy": {"no-referrer"}, "X-Frame-Options": {"DENY"},
			"Content-Security-Policy": {"default-src 'none'; script-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'"},
		}, Body: []byte(body)}, nil
	}
	if subtle.ConstantTimeCompare([]byte(session.ticket), []byte(req.Headers.Get("X-OAuth-Ticket"))) != 1 {
		return relayJSON(403, "error"), nil
	}
	switch req.Path {
	case relayBase + "/check":
		return relayJSON(200, session.status), nil
	case relayBase + "/submit":
		// Resource routes in the pinned SDK support GET only. Credentials
		// travel in a header, never in the URL, query, access log or referrer.
		if session.submitted {
			return relayJSON(200, "received"), nil
		}
		raw, err := base64.RawURLEncoding.DecodeString(req.Headers.Get("X-OAuth-Callback"))
		if err != nil || len(raw) > 8192 {
			return relayJSON(400, "error"), nil
		}
		var callback struct {
			State string `json:"state"`
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &callback) != nil || callback.State != state {
			return relayJSON(400, "error"), nil
		}
		if callback.Error == "" {
			var credentials commandCodeCredentials
			if json.Unmarshal([]byte(callback.Code), &credentials) != nil || credentials.APIKey == "" || credentials.UserID == "" || credentials.UserName == "" || credentials.KeyName == "" {
				return relayJSON(400, "error"), nil
			}
		}
		session.payload, session.submitted, session.status = raw, true, "received"
		return relayJSON(200, "received"), nil
	default:
		return relayJSON(404, "error"), nil
	}
}

func (p *CommandCodeGoPlugin) claimRelay(state string) []byte {
	p.loginMu.Lock()
	defer p.loginMu.Unlock()
	s := p.logins[state]
	if s == nil || time.Now().After(s.expires) {
		delete(p.logins, state)
		return nil
	}
	data := s.payload
	s.payload = nil
	return data
}

func (p *CommandCodeGoPlugin) finishRelay(state string, resp pluginapi.AuthLoginPollResponse, err error) {
	p.loginMu.Lock()
	defer p.loginMu.Unlock()
	if s := p.logins[state]; s != nil {
		if err != nil || resp.Status == pluginapi.AuthLoginStatusError {
			s.status = "error"
		}
		if resp.Status == pluginapi.AuthLoginStatusSuccess {
			s.status = "validated"
		}
	}
}
