package login

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	auth "github.com/cliproxyapi-plugins/model-quality/login/account"
	"github.com/cliproxyapi-plugins/model-quality/login/conversion"
)

func conversionSource(raw []byte, upload bool) (string, []int, error) {
	allowed := []string{"source"}
	if upload {
		allowed = append(allowed, "indexes")
	}
	r, err := Object(raw, allowed...)
	if err != nil || r.Get("source").Type != ojson.String || len(r.Get("source").String()) > conversion.MaxBytes {
		return "", nil, errors.New("invalid_request")
	}
	indexes := []int{}
	if upload {
		if !r.Get("indexes").IsArray() || len(r.Get("indexes").Array()) == 0 || len(r.Get("indexes").Array()) > conversion.MaxAccounts {
			return "", nil, errors.New("invalid_request")
		}
		seen := map[uint64]bool{}
		for _, value := range r.Get("indexes").Array() {
			n, err := accountUint(value)
			if err != nil || n >= conversion.MaxAccounts || seen[n] {
				return "", nil, errors.New("invalid_request")
			}
			seen[n] = true
			indexes = append(indexes, int(n))
		}
	}
	return r.Get("source").String(), indexes, nil
}

func (s *Service) conversionPreview(raw []byte) (conversion.Result, error) {
	source, _, err := conversionSource(raw, false)
	if err != nil {
		return conversion.Result{}, err
	}
	if err := s.accountReady(); err != nil {
		return conversion.Result{}, err
	}
	result, err := conversion.Convert([]byte(source), s.opts.Now())
	if err == nil {
		result.SourceRef = s.journal.fingerprint("conversion-source", source)
	}
	return result, err
}

// A conversion reserves only its principal while the SDK call is in flight.
type conversionLease struct {
	email, accountID, principal string
}

func (l conversionLease) matches(email, accountID, principal string) bool {
	return l.email != "" && strings.EqualFold(email, l.email) || accountID != "" && accountID == l.accountID || principal != "" && principal == l.principal
}

func (s *Service) reserveConversion(item conversion.Item) error {
	lease := conversionLease{item.Email, item.AccountID, (auth.LoginPrincipal{AccountID: item.AccountID, UserID: item.UserID}).Key()}
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.fault {
		return errors.New("storage_unavailable")
	}
	if s.activeConversion != nil {
		return errors.New("conflict")
	}
	guarded := map[string]bool{}
	for id, a := range s.accounts {
		if lease.matches(a.Credentials.Email, "", a.Principal) {
			if a.View.SecurityPending {
				return errors.New("security_pending")
			}
			guarded[id] = true
		}
	}
	rateKey := s.journal.fingerprint(item.Email)
	for _, job := range s.jobs {
		r := job.record
		matches := guarded[r.View.AccountID] || lease.matches(job.displayEmail, r.SecurityAccountID, r.AdoptedPrincipal) ||
			item.Email != "" && rateKey == r.RateKey
		if matches && (securityBlocks(r) || !terminal(r.View.Status) || r.View.Status == "commit_unknown" || r.View.Status == "saved_not_adopted") {
			return errors.New("security_pending")
		}
	}
	s.activeConversion = &lease
	return nil
}

func selectedConversions(raw []byte, now time.Time) ([]conversion.Item, error) {
	source, indexes, err := conversionSource(raw, true)
	if err != nil {
		return nil, err
	}
	converted, err := conversion.Convert([]byte(source), now)
	if err != nil {
		return nil, err
	}
	items := make([]conversion.Item, 0, len(indexes))
	for _, index := range indexes {
		if index >= len(converted.Items) || converted.Items[index].Status != "ready" {
			return nil, errors.New("invalid_request")
		}
		items = append(items, converted.Items[index])
	}
	return items, nil
}

type conversionOutcome struct {
	CommitRef string `json:"-"`
	Index     int    `json:"index"`
	Filename  string `json:"filename"`
	AuthID    string `json:"auth_id"`
	Code      string `json:"code"`
	Status    string `json:"status"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

func (s *Service) uploadConvertedItem(ctx context.Context, owner Owner, item conversion.Item, batchID string) conversionOutcome {
	out := conversionOutcome{Index: item.Index, Filename: item.Filename, Status: "failed"}
	commitID := "conversion:" + conversion.Identity(item)
	blocked, err := s.journal.unconfirmedConversion(commitID)
	if err != nil {
		out.Code = SafeCode(err)
		return out
	}
	if blocked {
		out.Status, out.Code = "uncertain", "commit_unknown"
		return out
	}
	if err := s.reserveConversion(item); err != nil {
		out.Code = SafeCode(err)
		return out
	}
	defer func() {
		s.mu.Lock()
		s.activeConversion = nil
		s.mu.Unlock()
	}()
	guard := func() error {
		if err := s.accountReady(); err != nil {
			return err
		}
		if !owner.Valid() || ctx.Err() != nil {
			return errors.New("cancelled")
		}
		return s.checkUploadPlans(&auth.Auth{Provider: "codex", Metadata: item.Credential}, auth.LoginSnapshot{}, nil)
	}
	prepared := ""
	result, uploadErr := s.opts.Manager.ImportConvertedForCommand(ctx, item, batchID, guard, func(ctx context.Context, id, identity string) error {
		prepared = id
		return owner.Prepare(ctx, id, identity, commitID)
	})
	if prepared != "" {
		if finishErr := s.finishCredentialWrite(prepared, commitID, result); finishErr != nil && uploadErr == nil {
			uploadErr = errors.New("quality_reconciliation_pending")
		}
	}
	out.AuthID, out.Code = result.AuthID, SafeCode(uploadErr)
	switch {
	case result.State == auth.ConversionAlreadyPresent || result.State == auth.ConversionPreviouslyUploaded:
		out.Status, out.Code = "existing", result.State
	case result.State == "commit_unknown" || result.State == "saved_not_adopted" || out.Code == "quality_reconciliation_pending":
		out.Status = "uncertain"
		if out.Code != "quality_reconciliation_pending" {
			out.CommitRef = commitID
		}
	case uploadErr == nil:
		out.Status, out.Enabled = "uploaded", &result.CredentialEnabled
	}
	return out
}

func isConversionPath(path string) bool {
	return path == "/conversion/preview" || path == "/conversion/upload"
}
