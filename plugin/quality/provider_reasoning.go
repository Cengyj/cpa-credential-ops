package quality

import (
	"strings"

	"github.com/MoeForge/moego/json"
)

// A present nil value means the exact catalog model has no thinking support.
// Missing model keys mean its capability is unknown; an empty object remains a
// non-nil configured support object, as it does in CPA.
type thinkingOptions struct {
	Levels         []string `json:"levels,omitempty"`
	ZeroAllowed    bool     `json:"zero_allowed,omitempty"`
	DynamicAllowed bool     `json:"dynamic_allowed,omitempty"`
	Min            int      `json:"min,omitempty"`
	Max            int      `json:"max,omitempty"`
}

func normalizedThinkingOptions(options *thinkingOptions) *thinkingOptions {
	if options == nil {
		return nil
	}
	out := *options
	out.Levels = nil
	seen := map[string]bool{}
	for _, value := range options.Levels {
		level := strings.ToLower(strings.TrimSpace(value))
		if level == "" {
			continue
		}
		switch level {
		case "none":
			out.ZeroAllowed = true
		case "auto":
			out.DynamicAllowed = true
		}
		if !seen[level] {
			out.Levels = append(out.Levels, level)
			seen[level] = true
		}
	}
	return &out
}

// Keep only the capability projection from authenticated route configuration.
// Names and secrets are never copied into the public reasoning map.
func addConfiguredReasoning(target *Target, route providerRoute) {
	models := configList(route.effective("models"))
	if !target.identityVerified || target.identityConflict {
		return
	}
	if route.family == "openai-compatibility" {
		// Custom channel catalogs merge public aliases across credentials. They
		// cannot establish capability for this route's upstream model.
		target.reasoningModelIDs = make(map[string]string)
	}
	if len(models) == 0 {
		return
	}
	byAlias := map[string]providerObject{}
	ambiguous := map[string]bool{}
	for _, model := range models {
		wire := configText(model["name"])
		alias := configText(model["alias"])
		if alias == "" {
			alias = wire
		}
		alias = strings.ToLower(alias)
		if previous, exists := byAlias[alias]; exists {
			if !strings.EqualFold(configText(previous["name"]), wire) {
				ambiguous[alias] = true
			}
			// CPA keeps the first capability for an identical upstream name.
			continue
		}
		byAlias[alias] = model
	}

	reasoning := make(map[string]*thinkingOptions)
	target.reasoningModelIDs = make(map[string]string)
	for _, id := range target.Models {
		alias := id
		if route.prefix != "" {
			alias = strings.TrimPrefix(alias, route.prefix+"/")
		}
		alias = strings.ToLower(alias)
		model := byAlias[alias]
		wire := route.wireModel(id)
		if wire == "" || model == nil || ambiguous[alias] {
			continue
		}
		if raw := model["thinking"]; raw != nil {
			encoded, err := json.Marshal(raw)
			var options thinkingOptions
			if err == nil && json.Unmarshal(encoded, &options) == nil {
				normalized := normalizedThinkingOptions(&options)
				safe := true
				for _, level := range append(options.Levels, normalized.Levels...) {
					if route.sensitiveText(level) {
						safe = false
						break
					}
				}
				if safe {
					reasoning[id] = normalized
				}
			}
			continue
		}
		// Official compatibility routes use these defaults when thinking is nil.
		if route.family == "openai-compatibility" && model["image"] != true {
			reasoning[id] = &thinkingOptions{Levels: []string{"low", "medium", "high"}}
			continue
		}
		if route.family == "openai-compatibility" {
			continue
		}
		// CPA resolves configured capability from the suffix-free upstream name.
		catalogID := wire
		if open := strings.LastIndexByte(catalogID, '('); open >= 0 && strings.HasSuffix(catalogID, ")") {
			catalogID = strings.TrimSpace(catalogID[:open])
		}
		target.reasoningModelIDs[id] = catalogID
	}
	target.Reasoning = reasoning
}
