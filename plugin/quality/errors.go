package quality

import (
	"errors"
	"net/http"
	"strings"
)

// classifyError accepts an explicit service code, never incidental text in a message.
func classifyError(err error) (string, int) {
	statuses := map[string]int{
		"sdk_unavailable":                     http.StatusServiceUnavailable,
		"management_key_invalid":              http.StatusUnprocessableEntity,
		"management_temporarily_banned":       http.StatusServiceUnavailable,
		"management_restricted":               http.StatusServiceUnavailable,
		"management_rate_limited":             http.StatusTooManyRequests,
		"management_network":                  http.StatusServiceUnavailable,
		"management_unavailable":              http.StatusServiceUnavailable,
		"management_session_required":         http.StatusServiceUnavailable,
		"connection_endpoint_invalid":         http.StatusUnprocessableEntity,
		"connection_endpoint_changed":         http.StatusConflict,
		"connection_store_invalid":            http.StatusServiceUnavailable,
		"connection_invalidation_save_failed": http.StatusInsufficientStorage,
		"deployment_key_invalid":              http.StatusUnprocessableEntity,
		"management_uncertain":                http.StatusBadGateway,
		"connection_save_unknown":             http.StatusInternalServerError,
		"connection_save_failed":              http.StatusInsufficientStorage,
		"not_found":                           http.StatusNotFound,
		"conflict":                            http.StatusConflict,
		"blocked":                             http.StatusConflict,
		"queue_full":                          http.StatusTooManyRequests,
		"budget_exhausted":                    http.StatusTooManyRequests,
		"storage_limit":                       http.StatusInsufficientStorage,
		"unsupported":                         http.StatusUnprocessableEntity,
		"unavailable":                         http.StatusServiceUnavailable,
		"gone":                                http.StatusGone,
		"invalid_request":                     http.StatusBadRequest,
		"invalid_cron":                        http.StatusBadRequest,
	}
	aliases := map[string]string{
		"management_http_401": "management_key_invalid", "management_http_403": "management_restricted", "management_http_429": "management_rate_limited",
		"management_http_500": "management_unavailable", "management_http_502": "management_unavailable", "management_http_503": "management_unavailable", "management_http_504": "management_unavailable",
		"action_conflict": "conflict", "identity_conflict": "conflict", "action_already_released": "conflict", "rule_already_owns_pause": "conflict",
		"operator_disabled": "blocked", "credential_deleted": "blocked", "requires_sign_in": "blocked",
		"home_controlled": "unsupported", "stable_credential_identity_unavailable": "unsupported", "quality_authority_unavailable": "unavailable",
		"manual_release_requires_active_management_request": "blocked", "manual_release_scope_mismatch": "blocked",
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		code, _, _ := strings.Cut(current.Error(), ":")
		if mapped, ok := aliases[code]; ok {
			code = mapped
		}
		if status, ok := statuses[code]; ok {
			return code, status
		}
	}
	var hostStatus interface{ StatusCode() int }
	if errors.As(err, &hostStatus) && hostStatus.StatusCode() >= 500 {
		return "unavailable", http.StatusServiceUnavailable
	}
	return "invalid_request", http.StatusBadRequest
}
