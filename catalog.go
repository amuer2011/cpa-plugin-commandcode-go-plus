package plugin

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	// catalogPath is the vendor's public model list. It is served by the
	// Provider API surface but does not require Provider API credentials, so
	// a Go-plan key can read it even though the plan cannot call
	// /provider/v1/chat/completions.
	catalogPath = "/provider/v1/models"
	// catalogTTL bounds how often a live refresh may hit the vendor. The list
	// changes on vendor releases, not per request; ten minutes keeps the CPA
	// panel current without turning every model lookup into an upstream call.
	catalogTTL = 10 * time.Minute
)

//go:embed assets/models-snapshot.json
var catalogSnapshotJSON []byte

// catalogModel mirrors one entry of GET /provider/v1/models.
type catalogModel struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	ContextLength      int64    `json:"context_length"`
	SupportedEndpoints []string `json:"supported_endpoints"`
}

// catalogDocument is the embedded build-time snapshot shipped with the plugin.
// It is the fallback whenever a live refresh has not happened yet: StaticModels
// has no HTTP client, so it can only ever serve embedded or cached data.
type catalogDocument struct {
	Source     string         `json:"source"`
	CapturedAt string         `json:"captured_at"`
	Models     []catalogModel `json:"models"`
}

type catalogResponse struct {
	Data []catalogModel `json:"data"`
}

// modelEntry is the routing view of one model: what the vendor expects
// upstream, what clients type, and which subscription tier serves it.
type modelEntry struct {
	Upstream  string // vendor identifier, e.g. "deepseek/deepseek-v4.1-flash-fast"
	Alias     string // client-facing short name, e.g. "deepseek-v4.1-flash-fast"
	Display   string // human name published by the catalog
	Context   int64  // context window in tokens (0 when unknown)
	Tier      string // minimum plan tier ("go".."max"), "" when unknown
	Vendor    string // vendor family, e.g. "deepseek", "openai", "anthropic"
	Available bool   // false when the vendor list advertises a model its backend refuses
}

// modelIndex is an immutable snapshot of entries plus lookup keys. Swapping a
// new index in is the only way the catalog changes; readers never see a
// half-built map.
type modelIndex struct {
	entries []modelEntry
	byKey   map[string]modelEntry
	fetched time.Time // zero for the embedded snapshot
}

// lookup resolves a client-supplied model name. Keys are catalogKey-normalised
// (provider prefix, thinking suffix and case removed), so
// "commandcode-go/deepseek-v4.1-flash-fast", "deepseek/deepseek-v4.1-flash-fast"
// and "DeepSeek-V4.1-Flash-Fast" all resolve to one entry.
func (i *modelIndex) lookup(model string) (modelEntry, bool) {
	if i == nil || len(i.byKey) == 0 {
		return modelEntry{}, false
	}
	key := catalogKey(model)
	if key == "" {
		return modelEntry{}, false
	}
	if entry, ok := i.byKey[key]; ok {
		return entry, true
	}
	// Vendor ids carry a namespace, short aliases do not: accept either
	// spelling for the same model.
	if entry, ok := i.byKey[baseModel(key)]; ok {
		return entry, true
	}
	return modelEntry{}, false
}

// newModelIndex indexes entries by their alias and by their full upstream id.
func newModelIndex(entries []modelEntry, fetched time.Time) *modelIndex {
	idx := &modelIndex{
		entries: entries,
		byKey:   make(map[string]modelEntry, len(entries)*2),
		fetched: fetched,
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Upstream) == "" {
			continue
		}
		if key := catalogKey(entry.Alias); key != "" {
			idx.byKey[key] = entry
		}
		idx.byKey[strings.ToLower(strings.TrimSpace(entry.Upstream))] = entry
	}
	return idx
}

// entriesFromCatalog converts a vendor list into routing entries. The alias is
// the lowercased basename, which the catalog keeps unique; a collision (should
// the vendor ever introduce one) falls back to the full id so no model is
// shadowed.
func entriesFromCatalog(models []catalogModel) []modelEntry {
	entries := make([]modelEntry, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		upstream := strings.TrimSpace(model.ID)
		if upstream == "" {
			continue
		}
		alias := strings.ToLower(baseModel(upstream))
		if _, duplicate := seen[alias]; duplicate {
			alias = strings.ToLower(upstream)
		}
		seen[alias] = struct{}{}
		entries = append(entries, modelEntry{
			Upstream:  upstream,
			Alias:     alias,
			Display:   strings.TrimSpace(model.Name),
			Context:   model.ContextLength,
			Tier:      modelTier(upstream),
			Vendor:    vendorFamily(upstream),
			Available: !modelUnavailable(upstream),
		})
	}
	return entries
}

// entriesFromConfig keeps the historical `models:` override working: an entry
// pairs a client-facing alias with the exact vendor name.
func entriesFromConfig(entries []ModelEntry) []modelEntry {
	out := make([]modelEntry, 0, len(entries))
	for _, entry := range entries {
		upstream := strings.TrimSpace(entry.Name)
		alias := strings.TrimSpace(entry.Alias)
		if upstream == "" {
			upstream = alias
		}
		if upstream == "" {
			continue
		}
		if alias == "" {
			alias = baseModel(upstream)
		}
		display := strings.TrimSpace(entry.DisplayName)
		if display == "" {
			display = upstream
		}
		out = append(out, modelEntry{
			Upstream:  upstream,
			Alias:     strings.ToLower(alias),
			Display:   display,
			Tier:      modelTier(upstream),
			Vendor:    vendorFamily(upstream),
			Available: !modelUnavailable(upstream),
		})
	}
	return out
}

// embeddedCatalog decodes the build-time snapshot. A corrupt embed yields an
// empty catalog rather than a panic: StaticModels then simply serves nothing
// and a live refresh fills the gap.
func embeddedCatalog() []catalogModel {
	var doc catalogDocument
	if err := json.Unmarshal(catalogSnapshotJSON, &doc); err != nil {
		return nil
	}
	return doc.Models
}

// fetchCatalog downloads the live model list through the host HTTP client so
// the request obeys the same transport policy as the executor.
func fetchCatalog(ctx context.Context, client pluginapi.HostHTTPClient, baseURL, apiKey string) ([]catalogModel, error) {
	if client == nil {
		return nil, fmt.Errorf("commandcode-go catalog: host HTTP client is required")
	}
	headers := http.Header{}
	headers.Set("Accept", "application/json")
	headers.Set("User-Agent", "cli")
	headers.Set("X-Command-Code-Version", "1.53.1")
	headers.Set("X-Cli-Environment", "production")
	if key := strings.TrimSpace(apiKey); key != "" {
		headers.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(ctx, pluginapi.HTTPRequest{
		Method:  http.MethodGet,
		URL:     strings.TrimSuffix(baseURL, "/") + catalogPath,
		Headers: headers,
	})
	if err != nil {
		return nil, fmt.Errorf("commandcode-go catalog: request failed: %w", err)
	}
	// Do not echo upstream bodies: they are vendor data, not diagnostics.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("commandcode-go catalog: endpoint returned HTTP %d", resp.StatusCode)
	}
	var parsed catalogResponse
	if err := json.Unmarshal(resp.Body, &parsed); err != nil {
		return nil, fmt.Errorf("commandcode-go catalog: invalid response body")
	}
	if len(parsed.Data) == 0 {
		return nil, fmt.Errorf("commandcode-go catalog: endpoint returned no models")
	}
	return parsed.Data, nil
}

// catalogKey normalises a model name for lookup: drop the plugin prefix, drop a
// thinking suffix such as "(high)", lowercase. The vendor namespace is kept —
// "zai-org/GLM-5.3" and "deepseek/deepseek-v4.1-flash" must not collide.
func catalogKey(model string) string {
	name := strings.TrimSpace(model)
	if name == "" {
		return ""
	}
	if i := strings.Index(name, "/"); i > 0 && strings.EqualFold(name[:i], Provider) {
		name = name[i+1:]
	}
	if i := strings.Index(name, "("); i >= 0 {
		name = strings.TrimSpace(name[:i])
	}
	return strings.ToLower(strings.TrimSpace(name))
}

// vendorFamily names the vendor a catalog entry belongs to, so whole families
// can be hidden. Namespaced ids use their first segment; namespace-less ids
// (claude-*, gpt-*) are mapped to the vendor that publishes them.
func vendorFamily(upstream string) string {
	name := strings.TrimSpace(upstream)
	if i := strings.Index(name, "/"); i > 0 {
		return strings.ToLower(name[:i])
	}
	lower := strings.ToLower(name)
	switch {
	case strings.HasPrefix(lower, "claude"):
		return "anthropic"
	case strings.HasPrefix(lower, "gpt"):
		return "openai"
	}
	return ""
}

// baseModel returns the last path segment of a model id.
func baseModel(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
}
