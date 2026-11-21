package quality

import (
	"regexp"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func isProviderTarget(t Target) bool { return t.IsProvider }

var providerIDSuffix = regexp.MustCompile(`^[0-9a-f]{12}(-[1-9][0-9]*)?$`)

// CPA uses kind:hash[-collision] IDs for configured routes. Match both the
// generated ID and its executor. A file credential may use the same executor.
func providerFamily(id, provider string) string {
	pos := strings.LastIndexByte(id, ':')
	if pos < 0 || !providerIDSuffix.MatchString(id[pos+1:]) {
		return ""
	}
	kind := id[:pos]
	if name, ok := strings.CutPrefix(kind, "openai-compatibility:"); ok && name != "" && compatibilityExecutor(name) == provider {
		return "openai-compatibility"
	}
	if kind == provider+":apikey" {
		switch provider {
		case "gemini", "vertex", "codex", "claude", "xai", "meta":
			return provider
		case "gemini-interactions":
			return "interactions"
		}
	}
	return ""
}

func compatibilityExecutor(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "openai-compatibility" {
		return "openai-compatibility"
	}
	if strings.HasPrefix(name, "openai-compatible-") {
		return name
	}
	return "openai-compatible-" + name
}

func configuredTarget(entry pluginapi.HostAuthModels, route providerRoute) Target {
	t := Target{AuthID: entry.ID, AuthIndex: entry.AuthIndex, Provider: entry.Provider, Type: "config_api_key", IsProvider: true, ObjectType: "provider",
		RuntimeStatus: "unknown", Models: []string{}, Reasons: []string{"provider_route_unverified"}, UnsupportedReason: "provider_observation_only"}
	if route.conflict || route.family != "" && (route.id != entry.ID || route.index != entry.AuthIndex || route.family != providerFamily(entry.ID, entry.Provider) || route.executor != entry.Provider) {
		t.Reasons = []string{"identity_conflict"}
		t.identityConflict = true
		// Retain a matching configured group for ownership checks without
		// borrowing names, models or capabilities from an ambiguous route.
		if route.id == entry.ID && route.index == entry.AuthIndex && route.executor == entry.Provider {
			t.ProviderGroupID = route.groupID
		}
		return t
	}
	if route.family == "" {
		return t
	}
	t.ProviderName = route.name
	t.ProviderGroupID, t.ProviderDisableUnit = route.groupID, route.unit
	t.ProviderProbeReason = route.probeReason()
	t.providerFamilyName = route.family
	if route.configModelsKnown {
		t.providerProbeModels = make(map[string]bool)
		for _, model := range route.models() {
			t.providerProbeModels[model] = route.wireModel(model) != ""
		}
	}
	t.identityVerified = true
	t.ManualDisabled = route.disabled
	t.catalogVerified = validCatalogModels(entry.Models)
	if t.catalogVerified {
		t.Models = append(t.Models, entry.Models...)
	}
	switch {
	case t.ManualDisabled:
		t.RuntimeStatus, t.Reasons = "disabled", []string{"operator_disabled"}
		if route.configModelsKnown {
			t.Models, t.catalogVerified = route.models(), true
		}
	case len(t.Models) > 0:
		// Registered routes are dispatched by CPA, which checks runtime
		// restrictions at selection time. The file status API cannot see them.
		t.RuntimeStatus, t.Reasons, t.EffectiveAvailable = "registered", []string{}, true
	default:
		t.Reasons = []string{"provider_models_unavailable"}
	}
	addConfiguredReasoning(&t, route)
	return t
}

func providerPlanProbeReason(t Target, models []string, parameters Parameters) string {
	if t.ProviderProbeReason != "" || !t.IsProvider {
		return t.ProviderProbeReason
	}
	if parameters.ReasoningEffort != "" {
		switch t.providerFamilyName {
		case "gemini", "interactions", "claude", "vertex":
			return "此供应商所选思考参数需要 CPA 原生转换，无法定向探测。"
		}
	}
	for _, model := range models {
		if t.providerProbeModels != nil && !t.providerProbeModels[model] && contains(t.Models, model) {
			return "无法唯一核实所选模型的上游映射。"
		}
	}
	return ""
}

func applyProviderRuntime(t *Target, current pluginapi.HostAuthFileEntry) {
	if current.Status == "" || t.ManualDisabled {
		return
	}
	t.RuntimeStatus = current.Status
	switch {
	case current.Disabled || current.Status == "disabled":
		t.ManualDisabled = true
		t.Reasons = append(t.Reasons, "operator_disabled")
	case current.Unavailable || current.Status != "active":
		t.Reasons = append(t.Reasons, "native_unavailable")
	case current.NextRetryAfter.After(time.Now()):
		t.Reasons = append(t.Reasons, "native_cooldown")
	}
	t.EffectiveAvailable = t.EffectiveAvailable && len(t.Reasons) == 0
}
