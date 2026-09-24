package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func (p *CommandCodeGoPlugin) DescribeQuota(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
	return pluginapi.QuotaDescribeResponse{SupportedProviders: []string{Provider}, DisplayName: "CommandCode"}, nil
}

type usageWindow struct {
	Used    float64 `json:"used"`
	Cap     float64 `json:"cap"`
	ResetAt int64   `json:"resetAt"`
}
type creditsResponse struct {
	Credits *struct {
		Monthly   float64 `json:"monthlyCredits"`
		Purchased float64 `json:"purchasedCredits"`
		Free      float64 `json:"freeCredits"`
	} `json:"credits"`
	Windows struct {
		FiveHour *usageWindow `json:"fiveHour"`
		Weekly   *usageWindow `json:"weekly"`
	} `json:"windowLimits"`
}

func (p *CommandCodeGoPlugin) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	out := pluginapi.QuotaFetchResponse{}
	key := credentialKey(req.StorageJSON, req.Metadata, req.Attributes)
	if key == "" {
		return out, fmt.Errorf("commandcode-go: missing auth file api_key")
	}
	if req.HTTPClient == nil {
		return out, fmt.Errorf("commandcode-go: host HTTP client is required")
	}
	fetch := func(path string, target any) error {
		resp, err := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: p.cfg.baseURL() + path, Headers: upstreamHeaders(key)})
		if err != nil {
			return fmt.Errorf("commandcode-go: quota request failed: %w", err)
		}
		// Do not echo upstream bodies: they may contain credentials or account details.
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("commandcode-go: quota endpoint returned HTTP %d", resp.StatusCode)
		}
		if err := json.Unmarshal(resp.Body, target); err != nil {
			return fmt.Errorf("commandcode-go: invalid quota response")
		}
		return nil
	}
	var identity struct {
		Org *struct {
			ID string `json:"id"`
		} `json:"org"`
	}
	if err := fetch("/alpha/whoami", &identity); err != nil {
		return out, err
	}
	suffix := ""
	if identity.Org != nil && identity.Org.ID != "" {
		suffix = "?orgId=" + url.QueryEscape(identity.Org.ID)
	}
	var credits creditsResponse
	if err := fetch("/alpha/billing/credits"+suffix, &credits); err != nil {
		return out, err
	}
	if credits.Credits == nil {
		return out, fmt.Errorf("commandcode-go: upstream did not return credit data")
	}
	var subscription struct {
		Data *struct {
			Plan             string `json:"planId"`
			Status           string `json:"status"`
			CurrentPeriodEnd string `json:"currentPeriodEnd"`
		} `json:"data"`
	}
	if err := fetch("/alpha/billing/subscriptions"+suffix, &subscription); err != nil {
		return out, err
	}
	if subscription.Data != nil {
		out.Subscription = &pluginapi.QuotaSubscription{Plan: subscription.Data.Plan, TierName: subscription.Data.Status}
		reset := ""
		if subscription.Data.CurrentPeriodEnd != "" {
			if parsed, err := time.Parse(time.RFC3339, subscription.Data.CurrentPeriodEnd); err == nil {
				reset = parsed.UTC().Format(time.RFC3339)
			}
		}
		out.Groups = append(out.Groups, pluginapi.QuotaGroup{DisplayName: "monthly", Buckets: []pluginapi.QuotaBucket{{Window: "monthly", RemainingFraction: 1, ResetTime: reset}}})
	}
	// Credits are provider credits, not a fabricated USD balance or inferred monthly cap.
	for _, metric := range []struct {
		key, label string
		value      float64
	}{
		{"remaining_credits", "Available credits", credits.Credits.Monthly + credits.Credits.Purchased + credits.Credits.Free},
		{"monthly_credits", "Subscription credits", credits.Credits.Monthly},
		{"purchased_credits", "Purchased credits", credits.Credits.Purchased},
		{"free_credits", "Free credits", credits.Credits.Free},
	} {
		out.Summary = append(out.Summary, pluginapi.QuotaMetric{Key: metric.key, Label: metric.label, Value: metric.value, Unit: "credits", Format: "number"})
	}
	for _, item := range []struct {
		name   string
		window *usageWindow
	}{{"fiveHour", credits.Windows.FiveHour}, {"weekly", credits.Windows.Weekly}} {
		w := item.window
		if w == nil || w.Cap <= 0 {
			continue
		}
		reset := ""
		if w.ResetAt > 0 {
			reset = time.UnixMilli(w.ResetAt).UTC().Format(time.RFC3339)
		}
		out.Groups = append(out.Groups, pluginapi.QuotaGroup{DisplayName: item.name, Buckets: []pluginapi.QuotaBucket{{Window: item.name, RemainingFraction: math.Max(0, math.Min(1, 1-w.Used/w.Cap)), ResetTime: reset, Description: fmt.Sprintf("%.6g / %.6g credits", w.Used, w.Cap)}}})
	}
	return out, nil
}
func (p *CommandCodeGoPlugin) ResetQuota(context.Context, pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
	return pluginapi.QuotaResetResponse{}, fmt.Errorf("commandcode-go: upstream quota cannot be reset locally")
}

var _ pluginapi.QuotaProvider = (*CommandCodeGoPlugin)(nil)
