package plugin

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Router hijacks commandcode-go-owned models to this plugin's executor.
// The host consults routers in priority order before built-in provider
// resolution; returning Handled=false falls through to the normal path.
type Router struct {
	cfg *pluginConfig
}

func NewRouter(cfg *pluginConfig) *Router { return &Router{cfg: cfg} }

func (r *Router) owned(req pluginapi.ModelRouteRequest) bool {
	set := r.cfg.claimed
	if set == nil {
		return false
	}
	for _, candidate := range []string{req.RequestedModel, modelFromBody(req.Body)} {
		if candidate == "" {
			continue
		}
		if _, ok := set[normalizeModel(candidate)]; ok {
			return true
		}
	}
	return false
}

// RouteModel routes commandcode-go models to this plugin's own executor.
func (r *Router) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
	_ = ctx
	if !r.owned(req) {
		return pluginapi.ModelRouteResponse{}, nil
	}
	if r.cfg.AuthFiles {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: Provider, TargetModel: Provider + "/" + normalizeModel(routeModel(req)), Reason: "CommandCode Go managed credential"}, nil
	}
	return pluginapi.ModelRouteResponse{
		Handled:    true,
		TargetKind: pluginapi.ModelRouteTargetSelf,
		Reason:     "commandcode-go provider plugin",
	}, nil
}

// modelFromBody extracts the model field from a raw client payload so the
// router also matches when RequestedModel is an alias the host already
// rewrote (or vice versa).
func modelFromBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	marker := []byte(`"model"`)
	idx := -1
	for i := 0; i+len(marker) <= len(body); i++ {
		match := true
		for j := range marker {
			if body[i+j] != marker[j] {
				match = false
				break
			}
		}
		if match {
			idx = i + len(marker)
			break
		}
	}
	if idx < 0 {
		return ""
	}
	rest := body[idx:]
	p := 0
	for p < len(rest) && (rest[p] == ' ' || rest[p] == '\t' || rest[p] == '\r' || rest[p] == '\n' || rest[p] == ':') {
		p++
	}
	if p >= len(rest) || rest[p] != '"' {
		return ""
	}
	p++
	var b strings.Builder
	for ; p < len(rest); p++ {
		c := rest[p]
		if c == '\\' && p+1 < len(rest) {
			p++
			b.WriteByte(rest[p])
			continue
		}
		if c == '"' {
			return b.String()
		}
		b.WriteByte(c)
	}
	return ""
}

// routeModel uses the payload only when the host did not supply a model name.
func routeModel(req pluginapi.ModelRouteRequest) string {
	if strings.TrimSpace(req.RequestedModel) != "" {
		return req.RequestedModel
	}
	return modelFromBody(req.Body)
}
