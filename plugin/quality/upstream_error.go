package quality

import (
	"net/http"
	"strings"
	"unicode"

	"github.com/MoeForge/moego/ojson"
	"github.com/MoeForge/moego/regexp"
)

// Both probe paths pass only the upstream status and headers. Provider bodies
// and header values stay local and are never copied into a sample.
func managedUpstreamCondition(status int, body, model string, header http.Header) string {
	switch status {
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusPaymentRequired:
		return "payment_required"
	case http.StatusBadRequest, http.StatusNotFound, http.StatusTooManyRequests:
	default:
		return ""
	}
	if len(body) > 16<<10 || status == http.StatusTooManyRequests && exhaustedRateCounter(header) {
		return ""
	}
	model = strings.TrimSpace(model)
	message := strings.TrimSpace(body)
	var code, kind string
	if strings.HasPrefix(message, "{") {
		if !ojson.Valid(message) || uniqueJSON([]byte(message)) != nil {
			return ""
		}
		fields := ojson.Parse(message)
		if status == http.StatusTooManyRequests && limitedQuotaFields(fields, 0) {
			return ""
		}
		if status != http.StatusTooManyRequests && conflictingErrorModel(fields, model, 0) {
			return ""
		}
		if nested := fields.Get("error"); nested.Exists() {
			if !nested.IsObject() {
				return ""
			}
			fields = nested
		}
		for _, key := range []string{"type", "code", "message"} {
			value := fields.Get(key)
			if value.Exists() && value.Raw != "null" && value.Type != ojson.String {
				return ""
			}
		}
		code = strings.ToLower(strings.TrimSpace(fields.Get("code").String()))
		kind = strings.ToLower(strings.TrimSpace(fields.Get("type").String()))
		message = fields.Get("message").String()
		if message == "" && fields.Get("detail").Type == ojson.String {
			message = fields.Get("detail").String()
		}
	}
	message = strings.TrimSuffix(strings.TrimSpace(message), ".")
	if status == http.StatusTooManyRequests {
		if limitedQuotaText(message) {
			return ""
		}
		for _, field := range []string{code, kind} {
			switch field {
			case "insufficient_quota", "quota_exhausted", "quota_exceeded", "usage_limit_reached", "billing_hard_limit_reached":
				return "quota_exhausted"
			}
		}
		switch strings.ToLower(message) {
		case "quota exhausted", "quota exceeded", "insufficient quota", "you have exhausted your quota",
			"you exceeded your current quota, please check your plan and billing details":
			return "quota_exhausted"
		}
		return ""
	}
	for _, wording := range [][2]string{
		{"the model ", " is not supported"},
		{"model ", " is not supported"},
		{"the ", " model is not supported when using codex with a chatgpt account"},
		{"the model ", " does not exist or you do not have access to it"},
		{"the model ", " does not exist"},
		{"model ", " does not exist"},
		{"the model ", " was not found"},
		{"model ", " was not found"},
		{"the model ", " not found"},
		{"model ", " not found"},
		{"model not found: ", ""},
		{"unknown model: ", ""},
	} {
		prefix, suffix := wording[0], wording[1]
		end := len(message) - len(suffix)
		if end >= len(prefix) && strings.EqualFold(message[:len(prefix)], prefix) && strings.EqualFold(message[end:], suffix) {
			name := message[len(prefix):end]
			for _, quote := range []string{"", "'", "\"", "`"} {
				if model != "" && name == quote+model+quote {
					return "model_unsupported"
				}
			}
			return ""
		}
	}
	for _, field := range []string{code, kind} {
		switch field {
		case "model_not_found", "model_not_supported", "unsupported_model", "model_not_found_error", "unknown_model", "model_does_not_exist":
			return "model_unsupported"
		}
	}
	switch strings.ToLower(message) {
	case "model not supported", "unsupported model", "model not found", "unknown model", "the requested model is not supported":
		return "model_unsupported"
	}
	return ""
}

func conflictingErrorModel(fields ojson.Result, model string, depth int) bool {
	if depth >= 8 {
		return true
	}
	array := fields.IsArray()
	conflict := false
	fields.ForEach(func(key, value ojson.Result) bool {
		switch strings.ToLower(key.String()) {
		case "model":
			conflict = value.Raw != "null" && (value.Type != ojson.String || strings.TrimSpace(value.String()) != model)
		case "error", "metadata", "detail", "details", "context":
			conflict = value.Type == ojson.JSON && conflictingErrorModel(value, model, depth+1)
		default:
			conflict = array && value.Type == ojson.JSON && conflictingErrorModel(value, model, depth+1)
		}
		return !conflict
	})
	return conflict
}

func exhaustedRateCounter(header http.Header) bool {
	for name, values := range header {
		switch strings.ToLower(name) {
		case "x-ratelimit-remaining-requests", "x-ratelimit-remaining-tokens",
			"anthropic-ratelimit-requests-remaining", "anthropic-ratelimit-tokens-remaining",
			"anthropic-ratelimit-input-tokens-remaining", "anthropic-ratelimit-output-tokens-remaining":
			for _, value := range values {
				if strings.TrimSpace(value) == "0" {
					return true
				}
			}
		}
	}
	return false
}

// A rate limit or a limit on an individual model does not establish that
// the account has exhausted its allowance, even alongside a quota error code.
func limitedQuotaFields(fields ojson.Result, depth int) bool {
	if depth >= 8 {
		return true
	}
	limited := false
	fields.ForEach(func(key, value ojson.Result) bool {
		if value.Raw == "null" || value.String() == "" {
			return true
		}
		switch strings.ToLower(key.String()) {
		case "model", "models", "rate_limit", "rate_limits", "retry_after", "retry_after_ms", "retry_after_seconds", "tpm", "rpm", "tps", "rps", "rpd", "tpd", "rph", "tph":
			limited = true
		case "scope", "limit_scope", "quota_scope":
			if value.Type == ojson.String {
				scope := strings.ToLower(strings.TrimSpace(value.String()))
				switch strings.TrimPrefix(strings.ReplaceAll(scope, "-", "_"), "per_") {
				case "deployment", "endpoint", "route", "region", "project":
					limited = true
				default:
					limited = limitedQuotaText(scope)
				}
			}
		case "code", "type", "reason", "message", "detail", "rate_limit_type",
			"param", "unit", "limit", "limit_type", "limit_name", "quota_metric", "metric", "dimension":
			if value.Type == ojson.String {
				limited = limitedQuotaText(value.String())
			}
		}
		if !limited && value.Type == ojson.JSON {
			limited = limitedQuotaFields(value, depth+1)
		}
		return !limited
	})
	return limited
}

// Match an explicit request/token window or a quota scoped to one part of
// the service. An account allowance may itself have a weekly billing reset.
var (
	requestOrTokenWindow = regexp.MustCompile(`\b((hourly|daily|weekly|monthly) (requests?|tokens?)|(requests?|tokens?) (per |each |an? )?(hour|day|week|month)s?)\b`)
	localQuotaScope      = regexp.MustCompile(`\b((deployments?|endpoints?|routes?|regions?|projects?)( level)? (quotas?|limits?)|(per|for|on|within) (this |that |the |your |an? |current |selected )?(deployment|endpoint|route|region|project)s?)\b`)
)

func limitedQuotaText(text string) bool {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) })
	for _, word := range words {
		switch word {
		case "model", "models", "tpm", "rpm", "rps", "tps", "rpd", "tpd", "rph", "tph", "rate", "retry", "retryable", "overloaded",
			"minute", "minutes", "second", "seconds", "concurrency", "concurrent", "backoff":
			return true
		}
	}
	normalized := strings.Join(words, " ")
	return normalized == "tokens" || normalized == "requests" ||
		strings.Contains(normalized, "too many requests") || strings.Contains(normalized, "try again") ||
		requestOrTokenWindow.MatchString(normalized) || localQuotaScope.MatchString(normalized)
}
