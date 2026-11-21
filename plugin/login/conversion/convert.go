// Package conversion maps explicit sub2api exports to CPA Codex credentials.
// It never exchanges tokens, starts a login or contacts an upstream service.
package conversion

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
)

const MaxBytes = 2 << 20
const MaxAccounts = 100

type Item struct {
	Index       int            `json:"index"`
	Name        string         `json:"name"`
	Email       string         `json:"email"`
	AccountID   string         `json:"account_id"`
	UserID      string         `json:"user_id,omitempty"`
	Plan        string         `json:"plan_type"`
	Filename    string         `json:"filename,omitempty"`
	Status      string         `json:"status"`
	Error       string         `json:"error,omitempty"`
	DuplicateOf *int           `json:"duplicate_of,omitempty"`
	Warnings    []string       `json:"warnings"`
	Credential  map[string]any `json:"credential,omitempty"`
}

type Result struct {
	SourceRef string `json:"source_ref,omitempty"`
	Items     []Item `json:"items"`
	Ready     int    `json:"ready"`
}

// ValidateJSON rejects duplicate keys and excessive nesting before any parsing.
func ValidateJSON(raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxBytes || !ojson.ValidBytes(raw) {
		return errors.New("conversion_invalid_json")
	}
	var visit func(ojson.Result, int) bool
	visit = func(v ojson.Result, depth int) bool {
		if depth > 32 {
			return false
		}
		ok := true
		keys := map[string]bool{}
		if v.IsObject() || v.IsArray() {
			v.ForEach(func(k, child ojson.Result) bool {
				if v.IsObject() && keys[k.String()] {
					ok = false
					return false
				}
				keys[k.String()] = true
				ok = visit(child, depth+1)
				return ok
			})
		}
		return ok
	}
	if !visit(ojson.ParseBytes(raw), 0) {
		return errors.New("conversion_invalid_json")
	}
	return nil
}

func Convert(raw []byte, now time.Time) (Result, error) {
	out := Result{Items: []Item{}}
	if err := ValidateJSON(raw); err != nil {
		return out, err
	}
	root := ojson.ParseBytes(raw)
	if root.Get("data.accounts").IsArray() {
		root = root.Get("data")
	}
	var entries []ojson.Result
	switch {
	case root.IsArray():
		entries = root.Array()
	case root.Get("accounts").IsArray():
		if v := root.Get("version"); v.Exists() && v.Raw != "0" && v.Raw != "1" {
			return out, errors.New("conversion_unsupported_version")
		}
		if v := root.Get("type"); v.Exists() && v.String() != "sub2api-data" && v.String() != "sub2api-bundle" && v.String() != "" {
			return out, errors.New("conversion_unsupported_format")
		}
		entries = root.Get("accounts").Array()
	case root.IsObject() && (root.Get("credentials").Exists() || root.Get("access_token").Exists()):
		entries = []ojson.Result{root}
	default:
		return out, errors.New("conversion_unsupported_format")
	}
	if len(entries) == 0 || len(entries) > MaxAccounts {
		return out, errors.New("conversion_account_limit")
	}
	seen := make(map[string]int)
	for index, entry := range entries {
		item := convertOne(entry, now)
		item.Index = index
		if item.Status == "ready" {
			if first, exists := seen[item.Filename]; exists {
				item.Status, item.Error, item.Credential = "invalid", "conversion_duplicate_source", nil
				item.DuplicateOf = &first
			} else {
				seen[item.Filename] = index
				out.Ready++
			}
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func convertOne(entry ojson.Result, now time.Time) Item {
	item := Item{Status: "invalid", Plan: "unknown", Warnings: []string{}}
	fail := func(code string) Item { item.Error = code; item.Credential = nil; return item }
	if !entry.IsObject() {
		return fail("conversion_unsupported_format")
	}
	if name := entry.Get("name"); name.Type == ojson.String && len(name.String()) <= 200 {
		item.Name = name.String()
	}
	c := entry.Get("credentials")
	wrapped := c.Exists()
	if !wrapped {
		c = entry
	}
	if !c.IsObject() {
		return fail("invalid_credentials")
	}
	platform := entry.Get("platform")
	if platform.Exists() && (platform.Type != ojson.String || platform.String() != "openai" && platform.String() != "codex") {
		return fail("conversion_unsupported_provider")
	}
	if kind := entry.Get("type"); kind.Exists() && (kind.Type != ojson.String || kind.String() != "oauth" && kind.String() != "codex") {
		return fail("conversion_unsupported_type")
	}
	values := map[string]string{}
	for _, key := range []string{"access_token", "refresh_token", "id_token", "email", "chatgpt_account_id", "account_id", "chatgpt_user_id", "user_id", "plan_type", "chatgpt_plan_type"} {
		v := c.Get(key)
		if !v.Exists() || v.Type == ojson.Null {
			continue
		}
		if v.Type != ojson.String || len(v.String()) > 128<<10 || strings.ContainsAny(v.String(), "\r\n\x00") || strings.TrimSpace(v.String()) != v.String() {
			return fail("invalid_credentials")
		}
		values[key] = v.String()
	}
	if values["access_token"] == "" || strings.ContainsAny(values["access_token"], " \t") {
		return fail("conversion_missing_access_token")
	}
	for _, key := range []string{"access_token", "refresh_token", "id_token"} {
		for _, ch := range values[key] {
			if ch < 33 || ch > 126 {
				return fail("conversion_invalid_token")
			}
		}
	}
	merge := func(dst *string, incoming string) bool {
		if incoming == "" {
			return true
		}
		if len(incoming) > 512 || strings.ContainsAny(incoming, "\r\n\x00") || strings.TrimSpace(incoming) != incoming || *dst != "" && *dst != incoming {
			return false
		}
		*dst = incoming
		return true
	}
	for _, key := range []string{"chatgpt_account_id", "account_id"} {
		if !merge(&item.AccountID, values[key]) {
			return fail("identity_conflict")
		}
	}
	for _, key := range []string{"chatgpt_user_id", "user_id"} {
		if !merge(&item.UserID, values[key]) {
			return fail("identity_conflict")
		}
	}
	if !merge(&item.Email, strings.ToLower(values["email"])) {
		return fail("invalid_credentials")
	}
	plan := values["plan_type"]
	if plan == "" {
		plan = values["chatgpt_plan_type"]
	}
	var accessExpiry time.Time
	var tokenAccount, tokenUser, fallbackSub string
	for _, key := range []string{"access_token", "id_token"} {
		token := values[key]
		if token == "" {
			continue
		}
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			return fail("conversion_invalid_token")
		}
		raw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || ValidateJSON(raw) != nil || !ojson.ParseBytes(raw).IsObject() {
			return fail("conversion_invalid_token")
		}
		claims := ojson.ParseBytes(raw)
		auth := claims.Get("https://api\\.openai\\.com/auth")
		if auth.Exists() && !auth.IsObject() {
			return fail("conversion_invalid_token")
		}
		for _, pair := range []struct {
			dst   *string
			v     ojson.Result
			email bool
		}{
			{&item.AccountID, auth.Get("chatgpt_account_id"), false},
			{&item.UserID, auth.Get("chatgpt_user_id"), false},
			{&item.UserID, auth.Get("user_id"), false},
			{&tokenAccount, auth.Get("chatgpt_account_id"), false},
			{&tokenUser, auth.Get("chatgpt_user_id"), false},
			{&tokenUser, auth.Get("user_id"), false},
			{&item.Email, claims.Get("email"), true},
			{&item.Email, claims.Get("https://api\\.openai\\.com/profile.email"), true},
		} {
			if !pair.v.Exists() || pair.v.Type == ojson.Null {
				continue
			}
			v := pair.v.String()
			if pair.email {
				v = strings.ToLower(v)
			}
			if pair.v.Type != ojson.String || !merge(pair.dst, v) {
				return fail("identity_conflict")
			}
		}
		if v := auth.Get("chatgpt_plan_type"); v.Exists() && v.Type != ojson.Null {
			if v.Type != ojson.String {
				return fail("invalid_credentials")
			}
			if plan == "" {
				plan = v.String()
			}
		}
		if key == "id_token" {
			if sub := claims.Get("sub"); sub.Exists() && sub.Type != ojson.Null {
				if sub.Type != ojson.String || !merge(&fallbackSub, sub.String()) {
					return fail("conversion_invalid_token")
				}
			}
		}
		if key == "access_token" && claims.Get("exp").Exists() {
			accessExpiry, err = timestamp(claims.Get("exp"))
			if err != nil {
				return fail("conversion_invalid_expiry")
			}
		}
	}
	if item.AccountID == "" {
		return fail("conversion_missing_account_id")
	}
	if tokenUser == "" {
		tokenUser = fallbackSub
	}
	// Explicit export metadata cannot replace the token identity required by
	// subsequent CPA account scans. Parsing claims does not verify signatures.
	if tokenAccount == "" || tokenUser == "" {
		return fail("identity_unverifiable")
	}
	if !merge(&item.UserID, tokenUser) {
		return fail("identity_conflict")
	}
	if item.Email == "" {
		item.Warnings = append(item.Warnings, "missing_email")
	} else if strings.Count(item.Email, "@") != 1 || strings.ContainsAny(item.Email, " \t") {
		return fail("invalid_credentials")
	}
	if len(plan) > 80 {
		return fail("invalid_credentials")
	}
	if plan != "" {
		item.Plan = plan
	}
	expiry := accessExpiry
	for _, key := range []string{"expires_at", "expired"} {
		if value := c.Get(key); value.Exists() && value.Type != ojson.Null && value.String() != "" {
			parsed, err := timestamp(value)
			if err != nil {
				return fail("conversion_invalid_expiry")
			}
			// Prefer the earliest known expiry; never extend a signed token lifetime.
			if expiry.IsZero() || parsed.Before(expiry) {
				expiry = parsed
			}
		}
	}
	if expiry.IsZero() {
		item.Warnings = append(item.Warnings, "missing_expiry")
	}
	if values["refresh_token"] == "" {
		item.Warnings = append(item.Warnings, "no_refresh_token")
	}
	if !expiry.IsZero() && !expiry.After(now) {
		if values["refresh_token"] == "" {
			return fail("conversion_expired_without_refresh")
		}
		item.Warnings = append(item.Warnings, "access_token_expired")
	}
	disabled := false
	for _, obj := range []ojson.Result{entry, c} {
		if value := obj.Get("disabled"); value.Exists() {
			if value.Type != ojson.True && value.Type != ojson.False {
				return fail("invalid_credentials")
			}
			disabled = disabled || value.Bool()
		}
	}
	disabled = disabled || entry.Get("status").String() == "inactive" || entry.Get("status").String() == "disabled"
	if disabled {
		item.Warnings = append(item.Warnings, "kept_disabled")
	}
	credential := map[string]any{"type": "codex", "access_token": values["access_token"], "account_id": item.AccountID, "disabled": disabled}
	for _, key := range []string{"refresh_token", "id_token"} {
		if values[key] != "" {
			credential[key] = values[key]
		}
	}
	if item.Email != "" {
		credential["email"] = item.Email
	}
	if item.UserID != "" {
		credential["user_id"] = item.UserID
	}
	if plan != "" {
		credential["plan_type"] = plan
	}
	if !expiry.IsZero() {
		credential["expired"] = expiry.UTC().Format(time.RFC3339)
	}
	if v := c.Get("last_refresh"); v.Exists() && v.String() != "" {
		t, err := timestamp(v)
		if err != nil {
			return fail("conversion_invalid_expiry")
		}
		credential["last_refresh"] = t.UTC().Format(time.RFC3339)
	}
	item.Filename = "codex-converted-" + Identity(item)[:24] + ".json"
	item.Status, item.Credential = "ready", credential
	return item
}

func Identity(item Item) string {
	user := item.UserID
	if user == "" {
		user = item.Email
	}
	sum := sha256.Sum256([]byte(item.AccountID + "\x00" + user))
	return hex.EncodeToString(sum[:])
}

func timestamp(v ojson.Result) (time.Time, error) {
	if v.Type != ojson.Number && v.Type != ojson.String {
		return time.Time{}, errors.New("invalid time")
	}
	s := v.String()
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 {
			n /= 1000
		}
		if n <= 0 || n > 253402300799 {
			return time.Time{}, errors.New("invalid time")
		}
		return time.Unix(n, 0).UTC(), nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 1970 {
		return time.Time{}, errors.New("invalid time")
	}
	return t.UTC(), nil
}
