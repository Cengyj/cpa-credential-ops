package login

import (
	"context"
	"errors"
	"strings"

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
	return conversion.Convert([]byte(source), s.opts.Now())
}

func (s *Service) uploadConversion(ctx context.Context, raw []byte) (any, error) {
	source, indexes, err := conversionSource(raw, true)
	if err != nil {
		return nil, err
	}
	converted, err := conversion.Convert([]byte(source), s.opts.Now())
	if err != nil {
		return nil, err
	}
	for _, index := range indexes {
		if index >= len(converted.Items) || converted.Items[index].Status != "ready" {
			return nil, errors.New("invalid_request")
		}
	}
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	if err := s.accountReady(); err != nil {
		return nil, err
	}
	owner, err := s.opts.Owner()
	if err != nil || owner.Valid == nil || owner.Prepare == nil || !owner.Valid() {
		return nil, errors.New("plugin_unavailable")
	}
	results := []map[string]any{}
	for _, index := range indexes {
		item := converted.Items[index]
		principal := auth.LoginPrincipal{AccountID: item.AccountID, UserID: item.UserID}
		guard := func() error {
			if err := s.accountReady(); err != nil {
				return err
			}
			if !owner.Valid() || s.ctx.Err() != nil || ctx.Err() != nil {
				return errors.New("cancelled")
			}
			for _, a := range s.accounts {
				matches := item.Email != "" && strings.EqualFold(a.Credentials.Email, item.Email) || a.Principal == principal.Key()
				if matches && (a.View.SecurityPending || s.accountBusyLocked(a)) {
					return errors.New("security_pending")
				}
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, job := range s.jobs {
				matches := item.Email != "" && (job.record.RateKey == s.journal.fingerprint(item.Email) || strings.EqualFold(job.displayEmail, item.Email)) || job.record.SecurityAccountID == item.AccountID || job.record.AdoptedPrincipal == principal.Key()
				if matches && (securityBlocks(job.record) || !terminal(job.record.View.Status) || job.record.View.Status == "commit_unknown" || job.record.View.Status == "saved_not_adopted") {
					return errors.New("security_pending")
				}
			}
			return s.checkUploadPlans(&auth.Auth{Provider: "codex", Metadata: item.Credential}, auth.LoginSnapshot{}, nil)
		}
		prepared := ""
		commitID := "conversion:" + conversion.Identity(item)
		result, uploadErr := s.opts.Manager.ImportConverted(ctx, item, guard, func(ctx context.Context, id, identity string) error {
			prepared = id
			return owner.Prepare(ctx, id, identity, commitID)
		})
		if prepared != "" {
			if finishErr := s.finishCredentialWrite(prepared, commitID, result); finishErr != nil && uploadErr == nil {
				uploadErr = errors.New("quality_reconciliation_pending")
			}
		}
		row := map[string]any{"index": index, "filename": item.Filename, "auth_id": result.AuthID, "code": SafeCode(uploadErr)}
		switch {
		case result.State == auth.ConversionAlreadyPresent || result.State == auth.ConversionPreviouslyUploaded:
			row["status"], row["code"] = "existing", result.State
		case result.State == "commit_unknown" || result.State == "saved_not_adopted":
			row["status"] = "uncertain"
		case uploadErr != nil:
			row["status"] = "failed"
		default:
			row["status"], row["enabled"] = "uploaded", result.CredentialEnabled
		}
		results = append(results, row)
	}
	return map[string]any{"items": results}, nil
}

func isConversionPath(path string) bool {
	return path == "/conversion/preview" || path == "/conversion/upload"
}
