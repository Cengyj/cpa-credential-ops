package login

import (
	"errors"
	"sort"

	auth "github.com/cliproxyapi-plugins/model-quality/login/account"
)

// Capture account metadata and visible history together, then release accountMu
// before consulting the SDK. These projections never enter admission or policy.
type accountProjection struct {
	view                 Account
	principal, emailHash string
}

func (s *Service) accountList(id string) ([]Account, error) {
	s.accountMu.Lock()
	if err := s.accountReady(); err != nil {
		s.accountMu.Unlock()
		return nil, err
	}
	if err := s.syncAccountJobsLocked(); err != nil {
		s.accountMu.Unlock()
		return nil, err
	}
	records := make([]storedAccount, 0, len(s.accounts))
	for key, a := range s.accounts {
		if id == "" || id == key {
			records = append(records, a)
		}
	}
	projections := s.projectAccountHistory(records)
	s.accountMu.Unlock()
	out := s.completeAccountProjections(projections)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Service) accountView(a storedAccount) Account {
	return s.completeAccountProjections(s.projectAccountHistory([]storedAccount{a}))[0]
}

func (s *Service) projectAccountHistory(records []storedAccount) []accountProjection {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest map[string]*session
	var pending map[string]string
	// Index a single pass only when an account has history worth projecting.
	for _, a := range records {
		if s.jobs[a.View.LastJobID] != nil {
			latest = make(map[string]*session)
			pending = make(map[string]string)
			break
		}
	}
	if latest != nil {
		for _, j := range s.jobs {
			r := &j.record
			id := r.View.AccountID
			if id == "" || r.HistoryDeleted {
				continue
			}
			if old := latest[id]; old == nil || newerAccountJob(r, &old.record) {
				latest[id] = j
			}
			if r.View.Status == "awaiting_commit" {
				pending[id] = r.View.ID
			}
		}
	}
	out := make([]accountProjection, 0, len(records))
	for _, a := range records {
		var job Job
		last := s.jobs[a.View.LastJobID]
		if last != nil && last.record.HistoryDeleted {
			last = latest[a.View.ID]
		}
		if last != nil {
			job = last.record.View
		}
		out = append(out, accountProjection{s.accountMetadata(a, job, pending[a.View.ID]), a.Principal, a.EmailHash})
	}
	return out
}

func (s *Service) completeAccountProjections(records []accountProjection) []Account {
	requested := make(map[string]struct{})
	for _, record := range records {
		if id := record.view.AuthID; id != "" {
			requested[id] = struct{}{}
		}
	}
	bindings := s.opts.Manager.DisplayBindings(requested)
	defaultProxy, _ := s.loginProxy.Load().(string)
	out := make([]Account, 0, len(records))
	for _, record := range records {
		v := record.view
		v.PlanType, _ = auth.ParsePlan(v.PlanType)
		bound := bindings[v.AuthID]
		if v.AuthID != "" {
			err := bound.Err
			if err == nil && (bound.Principal.Key() != record.principal || s.journal.fingerprint("account-email", bound.Principal.Email) != record.emailHash) {
				err = errors.New("identity_conflict")
			}
			if err != nil {
				v.BindingReadError, v.PlanType = SafeCode(err), "unknown"
			} else if bound.Plan != "unknown" {
				v.PlanType = bound.Plan
			}
		}
		var err error
		v.EffectiveProxyURL, v.ProxySource, err = displayLoginProxy(v.ProxyURL, defaultProxy, bound.ProxyURL, s.opts.Config.ProxyURL)
		v.ProxyError = SafeCode(err)
		out = append(out, v)
	}
	return out
}
