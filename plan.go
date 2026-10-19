package plugin

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

//go:embed assets/model-tiers.json
var modelTiersJSON []byte

// Plan tiers, cheapest first. A model's tier is the cheapest subscription that
// serves it; an account's tier comes from /alpha/billing/subscriptions.
//
// The vendor's Provider API does not publish this mapping (its /models list is
// account-agnostic), so it ships as data: assets/model-tiers.json, synced from
// the official plan pages. Keep it in step with the catalog snapshot.
const (
	tierGo = iota
	tierGoat
	tierPro
	tierProvider
	tierMax
)

var tierWeights = map[string]int{
	"go":       tierGo,
	"goat":     tierGoat,
	"pro":      tierPro,
	"provider": tierProvider,
	"max":      tierMax,
}

var tierNames = [...]string{"go", "goat", "pro", "provider", "max"}

// tierWeight maps a tier name to its rank.
func tierWeight(tier string) (int, bool) {
	weight, ok := tierWeights[strings.ToLower(strings.TrimSpace(tier))]
	return weight, ok
}

// tierName maps a rank back to a tier name.
func tierName(weight int) string {
	if weight < 0 || weight >= len(tierNames) {
		return ""
	}
	return tierNames[weight]
}

var (
	tierOnce sync.Once
	tierByID map[string]string
	blocked  map[string]struct{}
)

// modelTier returns the minimum subscription tier for a vendor model id.
func modelTier(upstream string) string {
	loadModelData()
	return tierByID[strings.ToLower(strings.TrimSpace(upstream))]
}

// modelUnavailable reports whether the catalog advertises a model the vendor's
// backend refuses to serve (missing backend, or a regional restriction). Such
// entries stay routable but are never advertised.
func modelUnavailable(upstream string) bool {
	loadModelData()
	_, ok := blocked[strings.ToLower(strings.TrimSpace(upstream))]
	return ok
}

// loadModelData parses the embedded tier table once.
func loadModelData() {
	tierOnce.Do(func() {
		var doc struct {
			Tiers       map[string]string `json:"tiers"`
			Unavailable []string          `json:"unavailable"`
		}
		if err := json.Unmarshal(modelTiersJSON, &doc); err != nil {
			return
		}
		tierByID = make(map[string]string, len(doc.Tiers))
		for id, tier := range doc.Tiers {
			tierByID[strings.ToLower(strings.TrimSpace(id))] = strings.ToLower(strings.TrimSpace(tier))
		}
		blocked = make(map[string]struct{}, len(doc.Unavailable))
		for _, id := range doc.Unavailable {
			if id = strings.ToLower(strings.TrimSpace(id)); id != "" {
				blocked[id] = struct{}{}
			}
		}
	})
}

// planPrefixes maps a subscription planId to its tier rank. The vendor encodes
// the plan in the id prefix ("individual-go-v1"), so matching is longest-prefix
// first: "individual-pro-v1" must win over "individual-pro".
var planPrefixes = []struct {
	prefix string
	weight int
}{
	{"individual-go-v1", tierGo},
	{"individual-go", tierGo},
	{"individual-goat", tierGoat},
	{"individual-pro-v1", tierPro},
	{"individual-pro", tierPro},
	{"individual-provider", tierProvider},
	{"individual-max", tierMax},
	{"individual-ultra", tierMax},
	{"teams-pro", tierPro},
}

// planTierWeight resolves a subscription planId to its tier rank.
func planTierWeight(planID string) (int, bool) {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(planID), "_", "-"))
	if normalized == "" {
		return 0, false
	}
	bestPrefix, weight, ok := 0, 0, false
	for _, plan := range planPrefixes {
		if strings.HasPrefix(normalized, plan.prefix) && len(plan.prefix) > bestPrefix {
			bestPrefix, weight, ok = len(plan.prefix), plan.weight, true
		}
	}
	return weight, ok
}

// activeSubscriptionStatuses are the statuses the vendor's CLI treats as live;
// a planId only counts while the subscription reports one of them.
var activeSubscriptionStatuses = map[string]struct{}{
	"active":   {},
	"trialing": {},
	"past_due": {},
}

// accountAccess is what one credential can actually run. Unknown data allows
// everything: a billing hiccup must not empty the model list.
type accountAccess struct {
	tierWeight int
	known      bool
	onDemand   bool
}

// allows reports whether the account's plan covers a model. Models absent from
// the tier table stay visible — better a visible model that 403s than a hidden
// model the account could run.
func (a accountAccess) allows(entry modelEntry) bool {
	if !a.known || a.onDemand {
		return true
	}
	weight, ok := tierWeight(entry.Tier)
	if !ok {
		return true
	}
	return weight <= a.tierWeight
}

// filterByAccess drops the entries the account's plan cannot serve.
func filterByAccess(entries []modelEntry, access accountAccess) []modelEntry {
	if !access.known || access.onDemand {
		return entries
	}
	out := make([]modelEntry, 0, len(entries))
	for _, entry := range entries {
		if access.allows(entry) {
			out = append(out, entry)
		}
	}
	return out
}

// filterByTier caps entries at an explicit tier rank (the models_tier knob).
func filterByTier(entries []modelEntry, maxWeight int) []modelEntry {
	out := make([]modelEntry, 0, len(entries))
	for _, entry := range entries {
		weight, ok := tierWeight(entry.Tier)
		if !ok || weight <= maxWeight {
			out = append(out, entry)
		}
	}
	return out
}

// fetchAccountAccess reads the credential's subscription tier and on-demand
// balance. The vendor's CLI does the same: whoami for the org scope, then the
// subscriptions and credits endpoints. Any failure reports "unknown" rather
// than an error — model discovery must not depend on billing.
func fetchAccountAccess(ctx context.Context, client pluginapi.HostHTTPClient, baseURL, apiKey string) (accountAccess, bool) {
	access := accountAccess{}
	if client == nil || strings.TrimSpace(apiKey) == "" {
		return access, false
	}
	base := strings.TrimSuffix(baseURL, "/")
	headers := upstreamHeaders(apiKey)
	get := func(path string, target any) error {
		resp, err := client.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: base + path, Headers: headers})
		if err != nil {
			return err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("commandcode-go plan: %s returned HTTP %d", path, resp.StatusCode)
		}
		if err := json.Unmarshal(resp.Body, target); err != nil {
			return fmt.Errorf("commandcode-go plan: invalid %s response", path)
		}
		return nil
	}

	var whoami struct {
		Org *struct {
			ID string `json:"id"`
		} `json:"org"`
	}
	orgID := ""
	if err := get("/alpha/whoami", &whoami); err == nil && whoami.Org != nil {
		orgID = strings.TrimSpace(whoami.Org.ID)
	}
	subscriptionPath := "/alpha/billing/subscriptions"
	if orgID != "" {
		subscriptionPath += "?orgId=" + url.QueryEscape(orgID)
	}

	var subscription struct {
		Data *struct {
			PlanID string `json:"planId"`
			Status string `json:"status"`
		} `json:"data"`
	}
	var credits struct {
		Credits *struct {
			PlanID    string  `json:"planId"`
			Purchased float64 `json:"purchasedCredits"`
			Free      float64 `json:"freeCredits"`
		} `json:"credits"`
	}
	subscriptionErr := get(subscriptionPath, &subscription)
	creditsErr := get("/alpha/billing/credits", &credits)
	if subscriptionErr != nil && creditsErr != nil {
		return access, false
	}

	planID := ""
	if subscription.Data != nil {
		if _, active := activeSubscriptionStatuses[strings.ToLower(strings.TrimSpace(subscription.Data.Status))]; active {
			planID = subscription.Data.PlanID
		}
	}
	if planID == "" && credits.Credits != nil {
		// The CLI stamps plan identity from credits too, for keys whose
		// subscription lookup is not permitted.
		planID = credits.Credits.PlanID
	}
	if weight, ok := planTierWeight(planID); ok {
		access.tierWeight, access.known = weight, true
	}
	if credits.Credits != nil && credits.Credits.Purchased+credits.Credits.Free > 0 {
		// On-demand credit can run models above the plan tier, so it disables
		// filtering entirely rather than raising the cap.
		access.onDemand = true
	}
	return access, access.known || access.onDemand
}
