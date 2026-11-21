// Package account adapts credential operations to the official native callbacks.
package account

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"strings"
)

// LoginPrincipal binds a login to a stable OpenAI account and user, including its workspace.
// Claims must come from the plugin's trusted token exchange; parsing is not signature verification.
type LoginPrincipal struct {
	AccountID string `json:"account_id"`
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
}

func (p LoginPrincipal) Key() string { return loginHash(p.AccountID, p.UserID) }

// LoginPrincipalFromAuth rejects missing or contradictory token identity claims.
func LoginPrincipalFromAuth(a *Auth) (LoginPrincipal, error) {
	var p LoginPrincipal
	if a == nil || a.Provider != "codex" {
		return p, errors.New("identity_unverifiable")
	}
	var fallbackSub string
	for _, key := range []string{"id_token", "access_token"} {
		token := authMetadataString(a, key)
		// Imported Codex exports can omit ID tokens. Access-token account and
		// user claims remain required; a present ID token must still agree.
		if key == "id_token" && token == "" {
			continue
		}
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			return p, errors.New("identity_unverifiable")
		}
		raw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || !ojson.ValidBytes(raw) {
			return p, errors.New("identity_unverifiable")
		}
		claims := ojson.ParseBytes(raw)
		authInfo := claims.Get("https://api\\.openai\\.com/auth")
		account := authInfo.Get("chatgpt_account_id").String()
		user := authInfo.Get("chatgpt_user_id").String()
		if user == "" {
			user = authInfo.Get("user_id").String()
		}
		email := strings.ToLower(strings.TrimSpace(claims.Get("email").String()))
		if email == "" {
			email = strings.ToLower(strings.TrimSpace(claims.Get("https://api\\.openai\\.com/profile.email").String()))
		}
		for _, pair := range []struct {
			dst   *string
			value string
		}{{&p.AccountID, account}, {&p.UserID, user}, {&p.Email, email}} {
			if pair.value == "" {
				continue
			}
			if *pair.dst != "" && *pair.dst != pair.value {
				return LoginPrincipal{}, errors.New("identity_conflict")
			}
			*pair.dst = pair.value
		}
		if key == "id_token" {
			fallbackSub = claims.Get("sub").String()
		}
	}
	if p.UserID == "" {
		p.UserID = fallbackSub
	}
	if p.AccountID == "" || p.UserID == "" {
		return LoginPrincipal{}, errors.New("identity_unverifiable")
	}
	if account := authMetadataString(a, "account_id"); account != "" && account != p.AccountID {
		return LoginPrincipal{}, errors.New("identity_conflict")
	}
	if email := strings.ToLower(authMetadataString(a, "email")); email != "" && p.Email != "" && email != p.Email {
		return LoginPrincipal{}, errors.New("identity_conflict")
	}
	return p, nil
}

// LoginSnapshot fingerprints the read result before acquiring new authorization.
// The official save callback cannot enforce this as an atomic compare-and-swap.
type LoginSnapshot struct {
	AuthID            string `json:"auth_id"`
	AuthIndex         string `json:"auth_index,omitempty"`
	IdentityVersion   string `json:"identity_version"`
	Name              string `json:"name"`
	PolicyFingerprint string `json:"policy_fingerprint"`
	Principal         string `json:"principal"`
}

// LoginCommitRequest contains an in-memory candidate from the plugin token exchange.
// Prepare stops dependent plugin work before saving through the official callback.
type LoginCommitRequest struct {
	CommitID         string
	Candidate        *Auth
	Expected         LoginSnapshot
	EnableCredential bool
	Guard            func() error
	Prepare          func(context.Context, string, string) error
}

// LoginCommitResult reports durable adoption, independently of credential availability.
type LoginCommitResult struct {
	CommitID          string `json:"commit_id"`
	AuthID            string `json:"auth_id"`
	AuthIndex         string `json:"auth_index,omitempty"`
	IdentityVersion   string `json:"identity_version"`
	Principal         string `json:"principal"`
	Name              string `json:"name,omitempty"`
	State             string `json:"state"`
	CredentialEnabled bool   `json:"credential_enabled"`
}

func loginHash(values ...string) string {
	h := sha256.New()
	for _, v := range values {
		h.Write([]byte(v))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func authMetadataString(a *Auth, key string) string {
	if a == nil {
		return ""
	}
	v, _ := a.Metadata[key].(string)
	return v
}
