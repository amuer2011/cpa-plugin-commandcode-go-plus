package plugin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func credentialKey(raw []byte, metadata map[string]any, attributes map[string]string) string {
	if key := strings.TrimSpace(attributes["api_key"]); key != "" {
		return key
	}
	if key, ok := metadata["api_key"].(string); ok && strings.TrimSpace(key) != "" {
		return strings.TrimSpace(key)
	}
	var data struct {
		APIKey string `json:"api_key"`
	}
	_ = json.Unmarshal(raw, &data)
	return strings.TrimSpace(data.APIKey)
}

func (p *CommandCodeGoPlugin) ParseAuth(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	var meta map[string]any
	if err := json.Unmarshal(req.RawJSON, &meta); err != nil {
		return pluginapi.AuthParseResponse{}, err
	}
	provider, _ := meta["type"].(string)
	if provider != Provider {
		return pluginapi.AuthParseResponse{}, nil
	}
	key := credentialKey(req.RawJSON, meta, nil)
	if key == "" {
		return pluginapi.AuthParseResponse{}, fmt.Errorf("commandcode-go: auth file requires api_key")
	}
	label, _ := meta["label"].(string)
	if label == "" {
		label, _ = meta["email"].(string)
	}
	disabled, _ := meta["disabled"].(bool)
	prefix, _ := meta["prefix"].(string)
	proxy, _ := meta["proxy_url"].(string)
	return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{Provider: Provider, FileName: req.FileName, Label: label, Disabled: disabled, Prefix: prefix, ProxyURL: proxy, StorageJSON: req.RawJSON, Metadata: meta, Attributes: map[string]string{"api_key": key}}}, nil
}

func (p *CommandCodeGoPlugin) StartLogin(_ context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("generate login state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(raw)
	callback, err := url.Parse(req.BaseURL)
	if err != nil || callback.Scheme != "https" || callback.Host == "" {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("CommandCode login requires a configured HTTPS callback URL")
	}
	if callback.Path != "/v0/management/oauth-callback" {
		return pluginapi.AuthLoginStartResponse{}, fmt.Errorf("CommandCode login requires the configured CPA management callback URL")
	}
	// Studio accepts only loopback callbacks. The browser-side bridge forwards to
	// the explicitly configured HTTPS management origin, never a request Host.
	callback = &url.URL{Scheme: "http", Host: "127.0.0.1:8765", Path: "/callback"}
	login := url.URL{Scheme: "https", Host: "commandcode.ai", Path: "/studio/auth/cli"}
	lq := login.Query()
	lq.Set("callback", callback.String())
	lq.Set("state", state)
	lq.Set("mode", "redirect")
	login.RawQuery = lq.Encode()
	return pluginapi.AuthLoginStartResponse{Provider: Provider, URL: login.String(), State: state, ExpiresAt: time.Now().Add(10 * time.Minute)}, nil
}

type commandCodeCredentials struct {
	APIKey   string `json:"apiKey"`
	UserID   string `json:"userId"`
	UserName string `json:"userName"`
	Email    string `json:"email"`
	KeyName  string `json:"keyName"`
}

// Keep OAuth credentials tied to an account-specific, path-safe email filename.
var commandCodeEmail = regexp.MustCompile(`^[A-Za-z0-9._%+\-]+@[A-Za-z0-9](?:[A-Za-z0-9.\-]*[A-Za-z0-9])?$`)

func commandCodeAuthFileName(email string) (string, error) {
	if len(email) > 254 || !commandCodeEmail.MatchString(email) || strings.Contains(email, "..") {
		return "", fmt.Errorf("CommandCode did not return a valid filename-safe email address")
	}
	return email + "-commandcode-go.json", nil
}

func (p *CommandCodeGoPlugin) PollLogin(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	state := strings.TrimSpace(req.State)
	if state == "" || strings.ContainsAny(state, "/\\") || state == "." || state == ".." {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Invalid login state"}, nil
	}
	file := filepath.Join(req.Host.AuthDir, ".oauth-"+Provider+"-"+state+".oauth")
	processing := file + ".processing"
	if err := rename(file, processing); err != nil {
		if isNotExist(err) {
			return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending}, nil
		}
		return pluginapi.AuthLoginPollResponse{}, fmt.Errorf("claim OAuth callback: %w", err)
	}
	defer removeFile(processing)
	data, err := readFile(processing)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{}, fmt.Errorf("read OAuth callback: %w", err)
	}
	var callback struct {
		Code  string `json:"code"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err = json.Unmarshal(data, &callback); err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Invalid OAuth callback data"}, nil
	}
	if callback.State != state {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "OAuth callback state mismatch"}, nil
	}
	if callback.Error != "" {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "CommandCode login was rejected"}, nil
	}
	var creds commandCodeCredentials
	if err = json.Unmarshal([]byte(callback.Code), &creds); err != nil || creds.APIKey == "" || creds.UserID == "" || creds.UserName == "" || creds.KeyName == "" {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Incomplete CommandCode credentials"}, nil
	}
	if req.HTTPClient == nil {
		return pluginapi.AuthLoginPollResponse{}, fmt.Errorf("host HTTP client unavailable")
	}
	resp, err := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: "https://api.commandcode.ai/alpha/whoami", Headers: http.Header{"Authorization": []string{"Bearer " + creds.APIKey}, "Accept": []string{"application/json"}}})
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "Could not validate CommandCode credentials"}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "CommandCode rejected the new API key"}, nil
	}
	// OAuth's userName can be a display name. The validated whoami response
	// carries the account email (optionally wrapped in a data object).
	var identity struct {
		User struct {
			Email string `json:"email"`
		} `json:"user"`
		Data struct {
			User struct {
				Email string `json:"email"`
			} `json:"user"`
		} `json:"data"`
	}
	_ = json.Unmarshal(resp.Body, &identity)
	email := strings.TrimSpace(identity.Data.User.Email)
	if email == "" {
		email = strings.TrimSpace(identity.User.Email)
	}
	if email == "" {
		email = strings.TrimSpace(creds.Email)
	}
	if email == "" {
		email = strings.TrimSpace(creds.UserName) // older OAuth responses may send the email here
	}
	fileName, err := commandCodeAuthFileName(email)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "CommandCode did not provide a usable email address for the auth file"}, nil
	}
	label := creds.UserName
	if creds.KeyName != "" {
		label += " (" + creds.KeyName + ")"
	}
	stored := map[string]any{"type": Provider, "api_key": creds.APIKey, "email": email, "label": label, "user_id": creds.UserID, "key_name": creds.KeyName}
	raw, err := json.Marshal(stored)
	if err != nil {
		return pluginapi.AuthLoginPollResponse{}, err
	}
	return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusSuccess, Auth: pluginapi.AuthData{Provider: Provider, FileName: fileName, Label: label, StorageJSON: raw, Metadata: stored, Attributes: map[string]string{"api_key": creds.APIKey}}}, nil
}

func (p *CommandCodeGoPlugin) RefreshAuth(_ context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Provider: Provider, ID: req.AuthID, StorageJSON: req.StorageJSON, Metadata: req.Metadata, Attributes: req.Attributes}}, nil
}

var _ pluginapi.AuthProvider = (*CommandCodeGoPlugin)(nil)
