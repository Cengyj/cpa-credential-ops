package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	json "github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/cliproxyapi-plugins/model-quality/login/conversion"
)

const (
	ConversionAlreadyPresent     = "conversion_already_present"
	ConversionPreviouslyUploaded = "conversion_previously_uploaded"
)

var errConversionIdentity = errors.New("duplicate_identity")

// ImportConverted only creates new files. Receipts consume an identity before
// the host write, so a lost response or restart cannot silently repeat a save.
func (m *Manager) ImportConverted(ctx context.Context, item conversion.Item, guard func() error, prepare func(context.Context, string, string) error) (LoginCommitResult, error) {
	return m.ImportConvertedForCommand(ctx, item, "", guard, prepare)
}

// CommandRef links this exact batch to the durable SDK receipt across databases.
func (m *Manager) ImportConvertedForCommand(ctx context.Context, item conversion.Item, commandRef string, guard func() error, prepare func(context.Context, string, string) error) (LoginCommitResult, error) {
	m.commitMu.Lock()
	defer m.commitMu.Unlock()
	result := LoginCommitResult{CommandRef: commandRef, CommitID: "conversion:" + conversion.Identity(item), AuthID: item.Filename, Principal: conversion.Identity(item), State: "aborted"}
	if item.Status != "ready" || item.Credential == nil || item.Filename == "" {
		return result, errors.New("invalid_request")
	}
	principal, err := LoginPrincipalFromAuth(&Auth{Provider: "codex", Metadata: item.Credential})
	if err != nil {
		return result, err
	}
	if principal.AccountID != item.AccountID || principal.UserID != item.UserID {
		return result, errors.New("identity_conflict")
	}
	doc := ojson.NewDocString(`{}`)
	for key, value := range item.Credential {
		if err := doc.Set(key, value); err != nil {
			return result, errors.New("invalid_credentials")
		}
	}
	canonical, err := canonicalJSON(doc.Bytes())
	if err != nil {
		return result, err
	}
	digest := sha256.Sum256([]byte(canonical))
	result.Principal = hex.EncodeToString(digest[:])
	old, err := m.LoginCommitStatus(result.CommitID)
	if err == nil {
		switch old.State {
		case "committed":
			if old.Principal != result.Principal {
				return result, errConversionIdentity
			}
		case "aborted":
			// The previous attempt ended before the save.
		default:
			return old, errors.New("commit_unknown")
		}
	} else if err.Error() != "not_found" {
		return result, errors.New("storage_unavailable")
	}
	check := func() error {
		if err := ctx.Err(); err != nil {
			return errors.New("cancelled")
		}
		if !m.Valid() {
			return errors.New("plugin_unavailable")
		}
		if guard != nil {
			return guard()
		}
		return nil
	}
	checkExisting := func() (bool, error) {
		current, err := m.existingConversion(item)
		if err != nil {
			if old.State == "committed" && errors.Is(err, errConversionIdentity) {
				return false, errors.New("conversion_previous_upload_changed")
			}
			return false, err
		}
		if old.State == "committed" && (current == nil || current.ID != old.AuthID || !convertedFieldsMatch(current, item)) {
			return false, errors.New("conversion_previous_upload_changed")
		}
		if current == nil {
			return false, nil
		}
		result.AuthID, result.State = current.ID, ConversionAlreadyPresent
		if old.State == "committed" {
			result.State = ConversionPreviouslyUploaded
		}
		return true, nil
	}
	if err := check(); err != nil {
		return result, err
	}
	if exists, err := checkExisting(); exists || err != nil {
		return result, err
	}
	if prepare != nil {
		if err := prepare(ctx, item.Filename, ""); err != nil {
			return result, err
		}
	}
	if err := check(); err != nil {
		return result, err
	}
	if exists, err := checkExisting(); exists || err != nil {
		return result, err
	}
	result.State = "commit_unknown"
	if err := m.receipt(result); err != nil {
		return result, errors.New("storage_unavailable")
	}
	if err := check(); err != nil {
		result.State = "aborted"
		if saveErr := m.receipt(result); saveErr != nil {
			result.State = "commit_unknown"
			return result, errors.New("commit_unknown")
		}
		return result, err
	}
	var saved struct {
		Name string `json:"name"`
	}
	if err := m.invoke("host.auth.save", map[string]any{"name": item.Filename, "json": json.RawMessage(doc.Bytes())}, &saved); err != nil {
		return result, errors.New("commit_unknown")
	}
	if saved.Name != item.Filename {
		return result, errors.New("commit_unknown")
	}
	entries, err := m.entries()
	if err != nil {
		return result, errors.New("commit_unknown")
	}
	for _, entry := range entries {
		if entry.Name != item.Filename {
			continue
		}
		actual, err := m.read(entry)
		if err != nil || actual.Provider != "codex" || !convertedFieldsMatch(actual, item) {
			return result, errors.New("commit_unknown")
		}
		var live struct {
			Auth Entry `json:"auth"`
		}
		if err := m.invoke("host.auth.get_runtime", map[string]string{"auth_index": actual.Index}, &live); err != nil {
			return result, errors.New("saved_not_adopted")
		}
		disabled, _ := item.Credential["disabled"].(bool)
		provider := live.Auth.Provider
		if provider == "" {
			provider = live.Auth.Type
		}
		if live.Auth.ID != actual.ID || live.Auth.Index != actual.Index || provider != "codex" || live.Auth.Status == "" || (live.Auth.Disabled || live.Auth.Status == "disabled") != disabled {
			return result, errors.New("saved_not_adopted")
		}
		result.AuthID, result.State, result.CredentialEnabled = actual.ID, "committed", !disabled
		result.AuthIndex = actual.Index
		result.Name = actual.FileName
		result.IdentityVersion = loginHash("identity", actual.ID, principal.Key())
		if err := m.receipt(result); err != nil {
			result.State = "commit_unknown"
			return result, errors.New("commit_unknown")
		}
		return result, nil
	}
	return result, errors.New("saved_not_adopted")
}

func convertedFieldsMatch(current *Auth, item conversion.Item) bool {
	for key, value := range item.Credential {
		if current.Metadata[key] != value {
			return false
		}
	}
	return true
}

// Only an unambiguous matching principal and grant can be shown as already
// present. A colliding filename, email, or stale grant never permits a save.
func (m *Manager) existingConversion(item conversion.Item) (*Auth, error) {
	entries, err := m.entries()
	if err != nil {
		return nil, err
	}
	var matched *Auth
	for _, entry := range entries {
		sameName := entry.Name == item.Filename || entry.ID == item.Filename
		sameEmail := item.Email != "" && strings.EqualFold(entry.Email, item.Email)
		codex := entry.Provider == "codex" || entry.Type == "codex"
		if !codex || entry.RuntimeOnly {
			if sameName || codex && sameEmail {
				return nil, errConversionIdentity
			}
			continue
		}
		current, err := m.read(entry)
		if err != nil {
			return nil, err
		}
		principal, principalErr := LoginPrincipalFromAuth(current)
		samePrincipal := principalErr == nil && principal.AccountID == item.AccountID && principal.UserID == item.UserID
		metadataEmail := authMetadataString(current, "email")
		metadataUser := authMetadataString(current, "user_id")
		sameEmail = sameEmail || item.Email != "" && (strings.EqualFold(metadataEmail, item.Email) || principal.Email == item.Email)
		legacyMatch := authMetadataString(current, "account_id") == item.AccountID && (principalErr != nil || metadataUser == item.UserID)
		if !sameName && !sameEmail && !samePrincipal && !legacyMatch {
			continue
		}
		if !samePrincipal || matched != nil || metadataUser != "" && metadataUser != item.UserID {
			return nil, errConversionIdentity
		}
		for _, email := range []string{entry.Email, metadataEmail, principal.Email} {
			if item.Email != "" && email != "" && !strings.EqualFold(email, item.Email) {
				return nil, errConversionIdentity
			}
		}
		for _, field := range []string{"access_token", "refresh_token", "id_token"} {
			want, _ := item.Credential[field].(string)
			got, present := current.Metadata[field]
			if (present || want != "") && got != want {
				return nil, errConversionIdentity
			}
		}
		matched = current
	}
	return matched, nil
}
