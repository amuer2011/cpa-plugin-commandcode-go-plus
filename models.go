package plugin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// billingTTL bounds how often one credential's plan is re-read. Plans change on
// billing events, not per request, and model discovery runs on every panel open.
const billingTTL = 5 * time.Minute

// ModelProvider contributes the commandcode-go model list to the host registry.
//
// StaticModels is served from the embedded/cached catalog snapshot because the
// host hands it no HTTP client. ModelsForAuth does have one, so it refreshes the
// live catalog and filters it by the credential's subscription tier.
type ModelProvider struct {
	cfg *pluginConfig

	mu      sync.Mutex
	billing map[string]billingRecord
}

type billingRecord struct {
	access accountAccess
	at     time.Time
}

func NewModelProvider(cfg *pluginConfig) *ModelProvider {
	return &ModelProvider{cfg: cfg, billing: make(map[string]billingRecord)}
}

func (p *ModelProvider) StaticModels(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
	index := p.cfg.modelIndex()
	if index == nil {
		return modelResponse(nil), nil
	}
	entries := advertised(index.entries, p.cfg)
	if maxWeight, ok := p.cfg.staticTierWeight(); ok {
		entries = filterByTier(entries, maxWeight)
	}
	return modelResponse(entries), nil
}

func (p *ModelProvider) ModelsForAuth(ctx context.Context, req pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
	key := credentialKey(req.StorageJSON, req.Metadata, req.Attributes)
	if key == "" && !p.cfg.AuthFiles {
		// Self-routed mode: the operator's configured key is the account.
		key = p.cfg.firstKey()
	}
	index := p.cfg.modelIndex()
	if p.cfg.catalogEnabled() && req.HTTPClient != nil {
		index = p.cfg.refreshCatalog(ctx, req.HTTPClient, key)
	}
	if index == nil {
		return modelResponse(nil), nil
	}
	entries := advertised(index.entries, p.cfg)
	if p.cfg.planFilterEnabled() && p.cfg.catalogEnabled() {
		if access, ok := p.accountAccess(ctx, req, key); ok {
			entries = filterByAccess(entries, access)
		}
	}
	return modelResponse(entries), nil
}

// advertised drops catalog entries that must not appear in a model list:
// models the vendor's backend cannot serve, and vendor families this CPA
// already reaches through its own accounts. Both stay routable — an explicit
// `commandcode-go/<id>` request still reaches the executor.
func advertised(entries []modelEntry, cfg *pluginConfig) []modelEntry {
	out := make([]modelEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.Available {
			continue
		}
		if cfg.catalogEnabled() && cfg.vendorExcluded(entry.Vendor) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// accountAccess resolves (and caches) the subscription tier behind one key.
func (p *ModelProvider) accountAccess(ctx context.Context, req pluginapi.AuthModelRequest, key string) (accountAccess, bool) {
	if req.HTTPClient == nil || key == "" {
		return accountAccess{}, false
	}
	cacheKey := credentialCacheKey(key)
	p.mu.Lock()
	record, hit := p.billing[cacheKey]
	p.mu.Unlock()
	if hit && time.Since(record.at) < billingTTL {
		return record.access, record.access.known || record.access.onDemand
	}
	access, ok := fetchAccountAccess(ctx, req.HTTPClient, p.cfg.baseURL(), key)
	if !ok {
		return accountAccess{}, false
	}
	p.mu.Lock()
	p.billing[cacheKey] = billingRecord{access: access, at: time.Now()}
	p.mu.Unlock()
	return access, true
}

// credentialCacheKey keeps plan facts off the raw credential: the map lives in
// memory for the plugin's lifetime, but hashing costs nothing.
func credentialCacheKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// visionModels is the set of upstream model ids that accept image input,
// keyed by lowercase id. It mirrors the DSH plugin's KNOWN_MODALITIES
// snapshot, which is derived from the official Command Code CLI model
// registry (inputModalities). Models absent from this set are treated as
// text-only: the registry is the authority, and guessing from a name would
// let the host send images to a model that rejects them.
var visionModels = map[string]bool{
	"claude-fable-5":                        true,
	"claude-fable-5-1":                      true,
	"claude-haiku-4-5-20251001":             true,
	"claude-opus-4-7":                       true,
	"claude-opus-4-8":                       true,
	"claude-opus-5":                         true,
	"claude-sonnet-4-6":                     true,
	"claude-sonnet-5":                       true,
	"deepseek/deepseek-v4-flash-vision-exp": true,
	"deepseek/deepseek-v4.1-flash":          true,
	"google/gemini-3.1-flash-lite":          true,
	"google/gemini-3.5-flash":               true,
	"google/gemini-3.5-flash-lite":          true,
	"google/gemini-3.6-flash":               true,
	"google/gemini-3.7-flash":               true,
	"google/gemini-3.8-flash":               true,
	"gpt-5.3-codex":                         true,
	"gpt-5.4":                               true,
	"gpt-5.4-mini":                          true,
	"gpt-5.5":                               true,
	"gpt-5.6-luna":                          true,
	"gpt-5.6-sol":                           true,
	"gpt-5.6-terra":                         true,
	"gpt-6-astra":                           true,
	"meta/muse-spark-1.1":                   true,
	"meta/muse-spark-1.2":                   true,
	"meta/muse-spark-1.2-contributor":       true,
	"meta/muse-spark-1.3":                   true,
	"meta/muse-spark-1.3-contributor":       true,
	"minimax/minimax-m3-free":               true,
	"minimaxai/minimax-m3":                  true,
	"moonshotai/kimi-k2.5":                  true,
	"moonshotai/kimi-k2.6":                  true,
	"moonshotai/kimi-k2.7-code":             true,
	"moonshotai/kimi-k2.7-code-highspeed":   true,
	"moonshotai/kimi-k3":                    true,
	"qwen/qwen3.6-plus":                     true,
	"qwen/qwen3.7-flash":                    true,
	"qwen/qwen3.7-plus":                     true,
	"qwen/qwen3.8-27b":                      true,
	"qwen/qwen3.8-flash":                    true,
	"qwen/qwen3.8-max":                      true,
	"qwen/qwen3.8-max-0902":                 true,
	"sakana/fugu-ultra":                     true,
	"stepfun/step-3.7-flash":                true,
	"thinkingmachines/inkling":              true,
	"thinkingmachines/inkling-small":        true,
	"xai/grok-4.5":                          true,
	"xai/grok-4.6":                          true,
	"xiaomi/mimo-v2.5":                      true,
	"z-ai/glm-5.3-flash":                    true,
}

// supportedModalities returns the input modalities a model accepts, from the
// CLI registry snapshot. Unknown ids default to text-only.
func supportedModalities(upstream string) []string {
	if visionModels[strings.ToLower(strings.TrimSpace(upstream))] {
		return []string{"text", "image"}
	}
	return []string{"text"}
}

// modelResponse turns catalogue entries into registry models. IDs use the
// commandcode-go namespace deliberately: the host skips plugin models that any
// native executor already serves, so reusing bare vendor names could leave this
// executor permanently unregistered.
func modelResponse(entries []modelEntry) pluginapi.ModelResponse {
	models := make([]pluginapi.ModelInfo, 0, len(entries))
	for _, entry := range entries {
		id := Provider + "/" + entry.Alias
		display := entry.Display
		if display == "" {
			display = entry.Upstream
		}
		models = append(models, pluginapi.ModelInfo{
			ID:                         id,
			Object:                     "model",
			OwnedBy:                    Provider,
			Type:                       "chat",
			DisplayName:                display + " via CommandCode Go",
			Name:                       id,
			Description:                entry.Upstream + " via CommandCode Go",
			InputTokenLimit:            entry.Context,
			SupportedGenerationMethods: []string{"chatCompletions"},
			SupportedInputModalities:   supportedModalities(entry.Upstream),
			SupportedOutputModalities:  []string{"text"},
			SupportedParameters:        []string{"temperature", "top_p", "max_tokens", "stop", "tools", "reasoning_effort"},
			Thinking: &pluginapi.ThinkingSupport{
				DynamicAllowed: true,
				Levels:         []string{"none", "auto", "low", "medium", "high", "max"},
			},
		})
	}
	return pluginapi.ModelResponse{Provider: Provider, Models: models}
}
