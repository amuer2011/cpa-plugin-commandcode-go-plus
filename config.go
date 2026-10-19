package plugin

import (
	"context"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// pluginConfig mirrors the plugins.configs.<id> mapping the host hands us
// as raw YAML at register/reconfigure time.
//
// Legacy self-routed mode reads plugin config. Managed mode delegates credential
// selection to the host so disabling or deleting an auth takes effect.
type pluginConfig struct {
	AuthFiles bool `yaml:"auth_files"`
	Enabled   bool `yaml:"enabled"`
	Priority  int  `yaml:"priority"`

	// APIKeys: first entry is used. A list keeps room for operators to swap
	// keys without restructuring; no pooling is implemented (one Go plan
	// account = one key).
	APIKeys []string `yaml:"api_keys"`

	// BaseURL overrides the upstream API root (tests, mirrors).
	BaseURL string `yaml:"base_url"`

	// Optional when the management frontend is hosted on a different origin.
	OAuthPublicURL string `yaml:"oauth_public_url"`

	// Models is an optional explicit override. When present it replaces the
	// live catalog: entries pair a client-facing alias with the exact vendor
	// name. When absent (the default) the model list comes from
	// GET /provider/v1/models, so new vendor models appear without a release.
	Models []ModelEntry `yaml:"models"`

	// ModelsTier caps the models advertised through StaticModels (the global
	// model page, which has no credential context): "", "all", or one of
	// go|goat|pro|provider|max. Empty means no cap.
	ModelsTier string `yaml:"models_tier"`

	// DisablePlanFilter turns off per-credential subscription filtering in
	// ModelsForAuth, so every catalog model is listed regardless of plan.
	DisablePlanFilter bool `yaml:"disable_plan_filter"`

	// ExcludeVendors hides whole vendor families from the advertised list.
	// Unset uses the default families (openai/anthropic/google) — the ones a
	// typical CPA deployment already serves through its own Codex, Claude or
	// Gemini accounts. An explicit empty list (`[]`) keeps every family.
	// Hidden models stay routable, so `commandcode-go/claude-sonnet-5-5` still
	// works for anyone who wants CommandCode's copy.
	ExcludeVendors []string `yaml:"exclude_vendors"`

	// index is the live model snapshot. It starts as the embedded catalog and
	// is replaced wholesale after a successful refresh, so readers always see
	// a consistent map without locking per lookup.
	indexMu   sync.RWMutex
	index     *modelIndex
	refreshMu sync.Mutex

	// blockedVendors is derived from ExcludeVendors at parse time (nil-safe).
	blockedVendors map[string]struct{}
}

// ModelEntry maps a client-facing alias to the name the vendor serves.
//
//	name  — the model name sent upstream ("deepseek/deepseek-v4.1-flash")
//	alias — the name clients request ("deepseek-v4.1-flash")
//
// The mapping is REQUIRED for overrides: this executor talks to /alpha/generate
// with its own base URL, so the host's alias table never applies, and the
// gateway rejects a bare alias ("deepseek-v4.1-flash is not recognized").
type ModelEntry struct {
	Alias       string `yaml:"alias"`
	Name        string `yaml:"name"`
	DisplayName string `yaml:"display_name"`
}

// UnmarshalYAML accepts the structured mapping and the bare-string form.
func (m *ModelEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		v := strings.TrimSpace(node.Value)
		m.Alias, m.Name = v, v
		return nil
	}
	type plain ModelEntry
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*m = ModelEntry(decoded)
	return nil
}

// label resolves the human-readable label for model registration.
func (m ModelEntry) label() string {
	if label := strings.TrimSpace(m.DisplayName); label != "" {
		return label
	}
	if name := strings.TrimSpace(m.Name); name != "" {
		return name
	}
	return strings.TrimSpace(m.Alias)
}

func parseConfig(raw []byte) *pluginConfig {
	cfg := &pluginConfig{}
	if len(raw) > 0 {
		_ = yaml.Unmarshal(raw, cfg)
	}
	cfg.blockedVendors = cfg.resolveExcludedVendors()
	cfg.index = cfg.initialIndex()
	return cfg
}

// defaultExcludedVendors are the families a CPA deployment usually serves from
// its own provider accounts. CommandCode resells the same models, so listing
// them again would duplicate ids the user already has (and confuse model
// pickers that key on the bare name).
var defaultExcludedVendors = []string{"openai", "anthropic", "google"}

// resolveExcludedVendors turns the YAML knob into a lookup set. Unset means the
// defaults; an explicit empty list means "keep every vendor".
func (c *pluginConfig) resolveExcludedVendors() map[string]struct{} {
	names := defaultExcludedVendors
	if c != nil && c.ExcludeVendors != nil {
		names = c.ExcludeVendors
	}
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			set[name] = struct{}{}
		}
	}
	return set
}

// vendorExcluded reports whether a vendor family is hidden from the list.
func (c *pluginConfig) vendorExcluded(vendor string) bool {
	if c == nil || vendor == "" {
		return false
	}
	_, ok := c.blockedVendors[vendor]
	return ok
}

// ownsModel reports whether a client-requested model belongs to this plugin.
//
// Catalog mode only claims namespaced ids (`commandcode-go/...`): the catalog
// carries models other providers also serve (gpt-*, claude-*, ...), and claiming
// their bare names would hijack requests meant for a native Codex/Claude auth.
// An explicit `models:` list keeps the old behaviour and owns its bare aliases.
func (c *pluginConfig) ownsModel(model string) bool {
	if _, ok := c.lookup(model); !ok {
		return false
	}
	if c.catalogEnabled() && !hasProviderPrefix(model) {
		return false
	}
	return true
}

// hasProviderPrefix reports whether the model already carries our namespace.
func hasProviderPrefix(model string) bool {
	model = strings.TrimSpace(model)
	return len(model) > len(Provider) &&
		strings.EqualFold(model[:len(Provider)], Provider) &&
		model[len(Provider)] == '/'
}

// initialIndex seeds the snapshot: the operator's explicit list when one is
// configured, otherwise the catalog embedded at build time. A live refresh
// replaces the latter on the first ModelsForAuth call.
func (c *pluginConfig) initialIndex() *modelIndex {
	if c != nil && len(c.Models) > 0 {
		return newModelIndex(entriesFromConfig(c.Models), time.Time{})
	}
	return newModelIndex(entriesFromCatalog(embeddedCatalog()), time.Time{})
}

// catalogEnabled reports whether the model list should track the vendor.
func (c *pluginConfig) catalogEnabled() bool {
	return c != nil && len(c.Models) == 0
}

// planFilterEnabled reports whether per-credential tier filtering applies.
func (c *pluginConfig) planFilterEnabled() bool {
	return c == nil || !c.DisablePlanFilter
}

// staticTierWeight resolves the models_tier cap for StaticModels.
func (c *pluginConfig) staticTierWeight() (int, bool) {
	if c == nil {
		return 0, false
	}
	switch name := strings.ToLower(strings.TrimSpace(c.ModelsTier)); name {
	case "", "all":
		return 0, false
	default:
		return tierWeight(name)
	}
}

// modelIndex returns the current immutable snapshot.
func (c *pluginConfig) modelIndex() *modelIndex {
	if c == nil {
		return nil
	}
	c.indexMu.RLock()
	defer c.indexMu.RUnlock()
	return c.index
}

// setModelIndex swaps in a freshly built snapshot.
func (c *pluginConfig) setModelIndex(index *modelIndex) {
	if c == nil || index == nil {
		return
	}
	c.indexMu.Lock()
	c.index = index
	c.indexMu.Unlock()
}

// refreshCatalog pulls the live model list when the snapshot is older than
// catalogTTL. Failures keep the previous snapshot: a vendor outage must not
// blank the model list.
func (c *pluginConfig) refreshCatalog(ctx context.Context, client pluginapi.HostHTTPClient, apiKey string) *modelIndex {
	if c == nil || !c.catalogEnabled() || client == nil {
		return c.modelIndex()
	}
	current := c.modelIndex()
	if current != nil && !current.fetched.IsZero() && time.Since(current.fetched) < catalogTTL {
		return current
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	// Another caller may have refreshed while we waited for the lock.
	if current = c.modelIndex(); current != nil && !current.fetched.IsZero() && time.Since(current.fetched) < catalogTTL {
		return current
	}
	models, err := fetchCatalog(ctx, client, c.baseURL(), apiKey)
	if err != nil {
		return c.modelIndex()
	}
	index := newModelIndex(entriesFromCatalog(models), time.Now())
	c.setModelIndex(index)
	return index
}

// lookup resolves a client-requested model through the current snapshot.
func (c *pluginConfig) lookup(model string) (modelEntry, bool) {
	return c.modelIndex().lookup(model)
}

// upstreamName returns the vendor's model name for a client-requested model.
// An empty result means "forward the client's name verbatim".
func (c *pluginConfig) upstreamName(model string) string {
	if entry, ok := c.lookup(model); ok {
		return entry.Upstream
	}
	return ""
}

// aliasFor returns the client-facing name for a requested model; unknown models
// fall back to the normalised request so routing stays predictable.
func (c *pluginConfig) aliasFor(model string) string {
	if entry, ok := c.lookup(model); ok {
		return entry.Alias
	}
	return normalizeModel(model)
}

// firstKey returns the configured API key ("" when none).
func (c *pluginConfig) firstKey() string {
	if c == nil {
		return ""
	}
	for _, k := range c.APIKeys {
		if k = strings.TrimSpace(k); k != "" {
			return k
		}
	}
	return ""
}

func (c *pluginConfig) baseURL() string {
	if c != nil && strings.TrimSpace(c.BaseURL) != "" {
		return strings.TrimSuffix(strings.TrimSpace(c.BaseURL), "/")
	}
	return upstreamBaseURL
}
