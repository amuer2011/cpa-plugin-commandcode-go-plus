package plugin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type quotaClient struct {
	requests []pluginapi.HTTPRequest
	status   int
	invalid  bool
}

func (c *quotaClient) Do(_ context.Context, r pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	c.requests = append(c.requests, r)
	status := c.status
	if status == 0 {
		status = 200
	}
	body := `{"org":{"id":"test org"}}`
	if strings.Contains(r.URL, "/credits") {
		body = `{"credits":{"monthlyCredits":8,"purchasedCredits":2,"freeCredits":0},"windowLimits":{"fiveHour":{"used":3,"cap":3,"resetAt":1790203719672},"weekly":{"used":1.5,"cap":6}}}`
	}
	if strings.Contains(r.URL, "/subscriptions") {
		body = `{"data":{"planId":"individual-go","status":"active","currentPeriodEnd":"2026-10-13T17:50:12.000Z"}}`
	}
	if c.invalid {
		body = `{}`
	}
	return pluginapi.HTTPResponse{StatusCode: status, Body: []byte(body)}, nil
}
func (*quotaClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	panic("unexpected streaming call")
}

func TestManagedAuth(t *testing.T) {
	_, p := Build([]byte("auth_files: true\napi_keys: [legacy-secret]"))
	resp, err := p.ParseAuth(context.Background(), pluginapi.AuthParseRequest{FileName: "account.json", RawJSON: []byte(`{"type":"commandcode-go","api_key":"test-key","disabled":true,"email":"test@example.test"}`)})
	if err != nil || !resp.Handled || !resp.Auth.Disabled || resp.Auth.Label != "test@example.test" {
		t.Fatalf("parse: %+v %v", resp, err)
	}
	if key := p.executor.requestKey(nil, nil, nil); key != "" {
		t.Fatal("managed mode must not fall back to legacy credentials")
	}
	if key := p.executor.requestKey(nil, resp.Auth.Metadata, resp.Auth.Attributes); key != "test-key" {
		t.Fatal("wrong selected credential")
	}
	route, err := p.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: "deepseek-v4.1-flash"})
	if err != nil || route.TargetKind != pluginapi.ModelRouteTargetProvider || route.Target != Provider || route.TargetModel != "commandcode-go/deepseek-v4.1-flash" {
		t.Fatalf("managed route: %+v %v", route, err)
	}
	_, err = p.ParseAuth(context.Background(), pluginapi.AuthParseRequest{RawJSON: []byte(`{"type":"commandcode-go"}`)})
	if err == nil {
		t.Fatal("missing credential accepted")
	}
	other, err := p.ParseAuth(context.Background(), pluginapi.AuthParseRequest{RawJSON: []byte(`{"type":"codex"}`)})
	if err != nil || other.Handled {
		t.Fatal("claimed unrelated credential")
	}
}
func TestQuotaNormalization(t *testing.T) {
	_, p := Build(nil)
	c := &quotaClient{}
	out, err := p.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Metadata: map[string]any{"api_key": "test-key"}, HTTPClient: c})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Summary) != 4 || out.Summary[0].Value != 10 || out.Subscription.Plan != "individual-go" {
		t.Fatalf("unexpected summary: %+v", out)
	}
	if len(out.Groups) != 3 || out.Groups[0].Buckets[0].Window != "monthly" || out.Groups[0].Buckets[0].ResetTime != "2026-10-13T17:50:12Z" || out.Groups[1].Buckets[0].RemainingFraction != 0 || out.Groups[2].Buckets[0].RemainingFraction != 0.75 {
		t.Fatalf("bad windows: %+v", out.Groups)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), `"remainingFraction":0`) {
		t.Fatal("empty quota must be retained")
	}
	for i, r := range c.requests {
		if r.Headers.Get("Authorization") != "Bearer test-key" {
			t.Fatal("missing selected key")
		}
		if i > 0 && !strings.Contains(r.URL, "orgId=test+org") {
			t.Fatal("missing organization scope")
		}
	}
	c.status = 401
	_, err = p.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Metadata: map[string]any{"api_key": "test-key"}, HTTPClient: c})
	if err == nil || !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "test-key") {
		t.Fatalf("unsafe or missing error: %v", err)
	}
	c.status = 200
	c.invalid = true
	if _, err = p.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Metadata: map[string]any{"api_key": "test-key"}, HTTPClient: c}); err == nil {
		t.Fatal("missing credits must not appear as zero")
	}
	if _, err = p.ResetQuota(context.Background(), pluginapi.QuotaResetRequest{}); err == nil {
		t.Fatal("must not promise an upstream reset")
	}
}
