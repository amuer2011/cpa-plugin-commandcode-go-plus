package plugin

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type quotaClient struct {
	requests            []pluginapi.HTTPRequest
	status              int
	invalid             bool
	creditBody          string
	internalCreditsBody string
	internalCreditsCode int
	subscriptionBody    string
}

func (c *quotaClient) Do(_ context.Context, r pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	c.requests = append(c.requests, r)
	status := c.status
	if status == 0 {
		status = 200
	}
	body := `{"org":{"id":"test org"}}`
	if strings.Contains(r.URL, "/internal/billing/credits") {
		body = c.internalCreditsBody
		if c.internalCreditsCode != 0 {
			status = c.internalCreditsCode
		} else if body == "" {
			status = 404
			body = `{}`
		}
	}
	if strings.Contains(r.URL, "/alpha/billing/credits") {
		body = c.creditBody
		if body == "" {
			body = `{"credits":{"monthlyCredits":8.229809344,"purchasedCredits":0,"freeCredits":0,"monthlyCreditsGranted":10},"windowLimits":{"fiveHour":{"used":0,"cap":3,"resetAt":0},"weekly":{"used":0.365735638,"cap":6,"resetAt":1790559185195}}}`
		}
	}
	if strings.Contains(r.URL, "/subscriptions") {
		body = c.subscriptionBody
		if body == "" {
			body = `{"data":{"planId":"individual-go","status":"active","currentPeriodEnd":"2026-10-13T17:50:12.000Z"}}`
		}
	}
	if c.invalid {
		body = `{}`
	}
	return pluginapi.HTTPResponse{StatusCode: status, Body: []byte(body)}, nil
}
func (*quotaClient) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	panic("unexpected streaming call")
}

func TestQuotaFallsBackToAlphaCredits(t *testing.T) {
	_, p := Build(nil)
	c := &quotaClient{
		internalCreditsCode: 404,
		creditBody:          `{"credits":{"monthlyCredits":8.229809344,"purchasedCredits":0,"freeCredits":0,"monthlyCreditsGranted":10},"windowLimits":{"fiveHour":{"used":0,"cap":3},"weekly":{"used":0.365735638,"cap":6}}}`,
	}
	out, err := p.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Metadata: map[string]any{"api_key": "test-key"}, HTTPClient: c})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Groups) != 3 || math.Abs(out.Groups[0].Buckets[0].RemainingFraction-0.8229809344) > 1e-9 {
		t.Fatalf("alpha fallback did not provide monthly quota: %+v", out.Groups)
	}
	foundFallback := false
	for _, request := range c.requests {
		if strings.Contains(request.URL, "/alpha/billing/credits") {
			foundFallback = true
		}
	}
	if !foundFallback {
		t.Fatal("expected fallback request to /alpha/billing/credits")
	}
}

func TestGoatMonthlyQuota(t *testing.T) {
	_, p := Build(nil)
	c := &quotaClient{
		internalCreditsBody: `{"credits":{"monthlyCredits":57.4,"purchasedCredits":0,"freeCredits":0,"premiumMonthlyCredits":0,"opensourceMonthlyCredits":57.4,"monthlyCreditsGranted":70},"windowLimits":{"limited":true,"fiveHour":{"used":2.8,"cap":14},"weekly":{"used":7,"cap":35}}}`,
		subscriptionBody:    `{"data":{"planId":"individual-goat","status":"active","currentPeriodEnd":"2026-10-13T17:50:12.000Z"}}`,
	}
	out, err := p.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Metadata: map[string]any{"api_key": "test-key"}, HTTPClient: c})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Groups) != 3 || math.Abs(out.Groups[0].Buckets[0].RemainingFraction-0.82) > 1e-9 || out.Groups[0].Buckets[0].Description != "12.6 / 70 credits used" {
		t.Fatalf("monthly GOAT quota was not normalized from the upstream grant: %+v", out.Groups)
	}
	if math.Abs(out.Groups[1].Buckets[0].RemainingFraction-0.8) > 1e-9 || math.Abs(out.Groups[2].Buckets[0].RemainingFraction-0.8) > 1e-9 {
		t.Fatalf("GOAT rolling windows were not normalized: %+v", out.Groups)
	}
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
	c := &quotaClient{internalCreditsBody: `{"credits":{"monthlyCredits":8.229809344,"purchasedCredits":0,"freeCredits":0,"premiumMonthlyCredits":0,"opensourceMonthlyCredits":8.229809344,"monthlyCreditsGranted":10},"windowLimits":{"limited":true,"exceeded":null,"fiveHour":{"used":0,"cap":3,"exceeded":false,"resetAt":0},"weekly":{"used":0.365735638,"cap":6,"exceeded":false,"resetAt":1790559185195}}}`}
	out, err := p.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{Metadata: map[string]any{"api_key": "test-key"}, HTTPClient: c})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Summary) != 4 || out.Summary[0].Value != 8.229809344 || out.Subscription.Plan != "individual-go" {
		t.Fatalf("unexpected summary: %+v", out)
	}
	if len(out.Groups) != 3 || out.Groups[0].Buckets[0].Window != "monthly" || out.Groups[0].Buckets[0].ResetTime != "2026-10-13T17:50:12Z" ||
		math.Abs(out.Groups[0].Buckets[0].RemainingFraction-0.8229809344) > 1e-9 || out.Groups[0].Buckets[0].Description != "1.77019 / 10 credits used" ||
		out.Groups[1].Buckets[0].RemainingFraction != 1 || math.Abs(out.Groups[2].Buckets[0].RemainingFraction-0.9390440603333333) > 1e-9 {
		t.Fatalf("bad windows: %+v", out.Groups)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), `"remainingFraction":0.8229809344`) {
		t.Fatal("monthly remaining fraction must be retained in serialized quota")
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
