package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// fakeHTTP is a scripted pluginapi.HostHTTPClient for catalog/plan tests.
type fakeHTTP struct {
	mu       sync.Mutex
	requests []string
	routes   map[string]string
	status   int
}

func (f *fakeHTTP) Do(_ context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req.URL)
	f.mu.Unlock()
	status := f.status
	if status == 0 {
		status = 200
	}
	for suffix, body := range f.routes {
		if strings.Contains(req.URL, suffix) {
			return pluginapi.HTTPResponse{StatusCode: status, Body: []byte(body)}, nil
		}
	}
	return pluginapi.HTTPResponse{StatusCode: 404, Body: []byte(`{}`)}, nil
}

func (f *fakeHTTP) DoStream(context.Context, pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	return pluginapi.HTTPStreamResponse{}, nil
}

func (f *fakeHTTP) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func testCatalog() []catalogModel {
	return []catalogModel{
		{ID: "deepseek/deepseek-v4.1-flash-fast", Name: "DeepSeek V4.1 Flash Fast (latest)", ContextLength: 1000000},
		{ID: "zai-org/GLM-5.3", Name: "GLM-5.3", ContextLength: 1000000},
		{ID: "claude-sonnet-5-5", Name: "Claude Sonnet 5.5", ContextLength: 1000000},
	}
}

func TestCatalogIndexLookup(t *testing.T) {
	cfg := parseConfig(nil)
	cfg.setModelIndex(newModelIndex(entriesFromCatalog(testCatalog()), time.Time{}))

	cases := map[string]string{
		"deepseek-v4.1-flash-fast":                "deepseek/deepseek-v4.1-flash-fast",
		"commandcode-go/deepseek-v4.1-flash-fast": "deepseek/deepseek-v4.1-flash-fast",
		"deepseek/deepseek-v4.1-flash-fast":       "deepseek/deepseek-v4.1-flash-fast",
		"DeepSeek-V4.1-Flash-Fast":                "deepseek/deepseek-v4.1-flash-fast",
		"deepseek-v4.1-flash-fast(high)":          "deepseek/deepseek-v4.1-flash-fast",
		"commandcode-go/zai-org/GLM-5.3":          "zai-org/GLM-5.3",
		"glm-5.3":                                 "zai-org/GLM-5.3",
		"commandcode-go/claude-sonnet-5-5":        "claude-sonnet-5-5",
	}
	for requested, want := range cases {
		if got := cfg.upstreamName(requested); got != want {
			t.Errorf("upstreamName(%q) = %q, want %q", requested, got, want)
		}
	}
	if got := cfg.upstreamName("no-such-model"); got != "" {
		t.Errorf("unknown model resolved to %q", got)
	}
	if got := cfg.aliasFor("zai-org/GLM-5.3"); got != "glm-5.3" {
		t.Errorf("aliasFor(GLM) = %q", got)
	}
}

func TestEntriesFromConfigPreservesAlias(t *testing.T) {
	cfg := parseConfig([]byte("models:\n  - alias: my-short\n    name: deepseek/deepseek-v4.1-flash\n"))
	if cfg.catalogEnabled() {
		t.Fatal("explicit models must disable the live catalog")
	}
	if got := cfg.upstreamName("commandcode-go/my-short"); got != "deepseek/deepseek-v4.1-flash" {
		t.Fatalf("upstreamName(alias) = %q", got)
	}
	if got := cfg.aliasFor("deepseek/deepseek-v4.1-flash"); got != "my-short" {
		t.Fatalf("aliasFor(upstream) = %q", got)
	}
}

func TestStaticModelsTierCap(t *testing.T) {
	cfg := parseConfig([]byte("models_tier: go\n"))
	cfg.setModelIndex(newModelIndex(entriesFromCatalog(testCatalog()), time.Time{}))
	provider := NewModelProvider(cfg)
	resp, err := provider.StaticModels(context.Background(), pluginapi.StaticModelRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("go-only list = %d models, want 2: %+v", len(resp.Models), resp.Models)
	}
	for _, model := range resp.Models {
		if strings.Contains(model.ID, "claude") {
			t.Fatalf("provider-tier model leaked into go list: %s", model.ID)
		}
	}
}

func TestPlanTierWeight(t *testing.T) {
	cases := map[string]struct {
		weight int
		ok     bool
	}{
		"individual-go-v1":    {tierGo, true},
		"individual-go":       {tierGo, true},
		"individual-goat":     {tierGoat, true},
		"individual-pro":      {tierPro, true},
		"individual-pro-v1":   {tierPro, true},
		"individual-provider": {tierProvider, true},
		"individual-max":      {tierMax, true},
		"individual_ultra":    {tierMax, true},
		"teams-pro":           {tierPro, true},
		"":                    {0, false},
		"enterprise-mystery":  {0, false},
	}
	for planID, want := range cases {
		weight, ok := planTierWeight(planID)
		if ok != want.ok || (ok && weight != want.weight) {
			t.Errorf("planTierWeight(%q) = %d,%v want %d,%v", planID, weight, ok, want.weight, want.ok)
		}
	}
}

func TestFilterByAccess(t *testing.T) {
	entries := entriesFromCatalog(testCatalog())
	goAccess := accountAccess{tierWeight: tierGo, known: true}
	proAccess := accountAccess{tierWeight: tierPro, known: true}
	onDemand := accountAccess{tierWeight: tierGo, known: true, onDemand: true}

	if got := len(filterByAccess(entries, goAccess)); got != 2 {
		t.Fatalf("go access kept %d entries, want 2", got)
	}
	if got := len(filterByAccess(entries, proAccess)); got != 3 {
		t.Fatalf("pro access kept %d entries, want 3", got)
	}
	if got := len(filterByAccess(entries, onDemand)); got != 3 {
		t.Fatalf("on-demand access kept %d entries, want 3", got)
	}
	if got := filterByAccess(entries, accountAccess{}); len(got) != 3 {
		t.Fatalf("unknown access must fail open, kept %d", len(got))
	}
}

func TestRefreshCatalogCachesAndFallsBack(t *testing.T) {
	cfg := parseConfig(nil)
	cfg.setModelIndex(newModelIndex(entriesFromCatalog(testCatalog()), time.Time{}))
	client := &fakeHTTP{routes: map[string]string{
		catalogPath: `{"object":"list","data":[` +
			`{"id":"deepseek/deepseek-v4.1-flash-fast","name":"DeepSeek V4.1 Flash Fast (latest)","context_length":1000000},` +
			`{"id":"zai-org/GLM-5.3","name":"GLM-5.3","context_length":1000000}]}`,
	}}

	index := cfg.refreshCatalog(context.Background(), client, "key")
	if index == nil || len(index.entries) != 2 || index.fetched.IsZero() {
		t.Fatalf("refresh did not install the live catalog: %+v", index)
	}
	cfg.refreshCatalog(context.Background(), client, "key")
	if got := client.count(); got != 1 {
		t.Fatalf("catalog was fetched %d times, want 1 (TTL cache)", got)
	}

	failing := &fakeHTTP{status: 500}
	stale := parseConfig(nil)
	stale.setModelIndex(newModelIndex(entriesFromCatalog(testCatalog()), time.Time{}))
	before := len(stale.modelIndex().entries)
	after := stale.refreshCatalog(context.Background(), failing, "key")
	if after == nil || len(after.entries) != before {
		t.Fatalf("failed refresh dropped the snapshot: %+v", after)
	}
}

func TestModelsForAuthFiltersByPlan(t *testing.T) {
	// Vendor exclusions are off here: this test isolates the plan filter.
	cfg := parseConfig([]byte("exclude_vendors: []\n"))
	cfg.setModelIndex(newModelIndex(entriesFromCatalog(testCatalog()), time.Time{}))
	provider := NewModelProvider(cfg)

	client := &fakeHTTP{routes: map[string]string{
		catalogPath: `{"object":"list","data":[` +
			`{"id":"deepseek/deepseek-v4.1-flash-fast","name":"DeepSeek V4.1 Flash Fast (latest)","context_length":1000000},` +
			`{"id":"claude-sonnet-5-5","name":"Claude Sonnet 5.5","context_length":1000000}]}`,
		"/alpha/whoami":          `{"org":{"id":"org-1"}}`,
		"/subscriptions":         `{"data":{"planId":"individual-go-v1","status":"active"}}`,
		"/alpha/billing/credits": `{"credits":{"planId":"individual-go-v1","purchasedCredits":0,"freeCredits":0}}`,
	}}
	req := pluginapi.AuthModelRequest{
		StorageJSON: []byte(`{"api_key":"test-key"}`),
		HTTPClient:  client,
	}
	resp, err := provider.ModelsForAuth(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Models) != 1 || resp.Models[0].ID != "commandcode-go/deepseek-v4.1-flash-fast" {
		t.Fatalf("go account models = %+v", resp.Models)
	}

	// The plan lookup is cached per key: a second call must not re-hit billing.
	before := client.count()
	if _, err := provider.ModelsForAuth(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if client.count() != before {
		t.Fatalf("plan lookup was not cached: %d -> %d requests", before, client.count())
	}

	proClient := &fakeHTTP{routes: map[string]string{
		catalogPath: `{"object":"list","data":[` +
			`{"id":"deepseek/deepseek-v4.1-flash-fast","name":"DeepSeek V4.1 Flash Fast (latest)","context_length":1000000},` +
			`{"id":"claude-sonnet-5-5","name":"Claude Sonnet 5.5","context_length":1000000}]}`,
		"/alpha/whoami":          `{"org":{"id":"org-1"}}`,
		"/subscriptions":         `{"data":{"planId":"individual-pro-v1","status":"active"}}`,
		"/alpha/billing/credits": `{"credits":{"planId":"individual-pro-v1"}}`,
	}}
	proCfg := parseConfig([]byte("exclude_vendors: []\n"))
	proCfg.setModelIndex(newModelIndex(entriesFromCatalog(testCatalog()), time.Time{}))
	proResp, err := NewModelProvider(proCfg).ModelsForAuth(context.Background(), pluginapi.AuthModelRequest{
		StorageJSON: []byte(`{"api_key":"pro-key"}`),
		HTTPClient:  proClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(proResp.Models) != 2 {
		t.Fatalf("pro account models = %+v", proResp.Models)
	}
}

func TestAccountAccessFallsBackToCreditsPlanID(t *testing.T) {
	client := &fakeHTTP{routes: map[string]string{
		"/alpha/whoami":          `{}`,
		"/subscriptions":         `{}`,
		"/alpha/billing/credits": `{"credits":{"planId":"individual-goat","purchasedCredits":2,"freeCredits":0}}`,
	}}
	access, ok := fetchAccountAccess(context.Background(), client, upstreamBaseURL, "key")
	if !ok || access.tierWeight != tierGoat || !access.onDemand {
		t.Fatalf("access = %+v ok=%v", access, ok)
	}
}

func TestUnavailableModelsAreHiddenButRoutable(t *testing.T) {
	cfg := parseConfig(nil)
	entries := entriesFromCatalog(embeddedCatalog())
	visible := advertised(entries, cfg)
	if len(unavailableIDs()) == 0 {
		t.Fatal("tier data declares no unavailable models")
	}
	for _, entry := range visible {
		if modelUnavailable(entry.Upstream) {
			t.Fatalf("unavailable model advertised: %s", entry.Upstream)
		}
	}
	for _, hidden := range []string{"zai-org/GLM-5.1", "Qwen/Qwen3.6-Max-Preview"} {
		for _, entry := range visible {
			if entry.Upstream == hidden {
				t.Fatalf("%s should be hidden", hidden)
			}
		}
	}
	// Hidden models stay routable: an explicit request still reaches the
	// executor, so a vendor fix does not require a plugin release.
	if got := cfg.upstreamName("commandcode-go/zai-org/GLM-5.1"); got != "zai-org/GLM-5.1" {
		t.Fatalf("hidden model lost its route: %q", got)
	}
	if modelTier("zai-org/GLM-5.1") == "" {
		t.Fatal("hidden model lost its tier")
	}
}

func TestVendorFamiliesAreHiddenByDefault(t *testing.T) {
	entries := entriesFromCatalog(embeddedCatalog())
	cfg := parseConfig(nil)
	visible := advertised(entries, cfg)
	for _, entry := range visible {
		if cfg.vendorExcluded(entry.Vendor) {
			t.Fatalf("excluded vendor leaked: %s (%s)", entry.Upstream, entry.Vendor)
		}
	}
	for _, hidden := range []string{"gpt-5.4", "claude-sonnet-5-5", "google/gemini-3.5-flash"} {
		for _, entry := range visible {
			if entry.Upstream == hidden {
				t.Fatalf("%s should be hidden by the default vendor scope", hidden)
			}
		}
	}
	// `exclude_vendors: []` opts back in to the full catalog.
	all := parseConfig([]byte("exclude_vendors: []\n"))
	if got := len(advertised(entries, all)); got <= len(visible) {
		t.Fatalf("explicit empty vendor list did not widen the catalog: %d <= %d", got, len(visible))
	}
}

func TestRouterClaimsOnlyNamespacedModels(t *testing.T) {
	_, p := Build([]byte("auth_files: true\n"))
	cases := map[string]bool{
		"gpt-5.4":                            false, // native Codex model, must fall through
		"claude-sonnet-5-5":                  false, // native Claude model
		"deepseek-v4.1-flash":                false, // bare alias is not advertised any more
		"commandcode-go/deepseek-v4.1-flash": true,
		"commandcode-go/gpt-5.4":             true, // hidden but still routable on request
	}
	for requested, want := range cases {
		route, err := p.RouteModel(context.Background(), pluginapi.ModelRouteRequest{RequestedModel: requested})
		if err != nil {
			t.Fatalf("%s: %v", requested, err)
		}
		if route.Handled != want {
			t.Fatalf("route(%q).Handled = %v, want %v", requested, route.Handled, want)
		}
		if want && route.TargetModel != "commandcode-go/"+strings.ToLower(baseModel(requested)) {
			t.Fatalf("route(%q).TargetModel = %q", requested, route.TargetModel)
		}
	}
}

// unavailableIDs lists the ids the tier data marks as not servable.
func unavailableIDs() []string {
	var doc struct {
		Unavailable []string `json:"unavailable"`
	}
	if err := json.Unmarshal(modelTiersJSON, &doc); err != nil {
		return nil
	}
	return doc.Unavailable
}

func TestEmbeddedCatalogCoversKnownModels(t *testing.T) {
	models := embeddedCatalog()
	if len(models) < 50 {
		t.Fatalf("embedded snapshot has %d models", len(models))
	}
	ids := make(map[string]struct{}, len(models))
	for _, model := range models {
		ids[model.ID] = struct{}{}
		if modelTier(model.ID) == "" {
			t.Fatalf("model %s has no plan tier", model.ID)
		}
	}
	for _, want := range []string{"deepseek/deepseek-v4.1-flash-fast", "zai-org/GLM-5.3", "moonshotai/Kimi-K3"} {
		if _, ok := ids[want]; !ok {
			t.Fatalf("embedded snapshot is missing %s", want)
		}
	}
	if fmt.Sprint(len(embeddedCatalog())) == "0" {
		t.Fatal("empty snapshot")
	}
}
