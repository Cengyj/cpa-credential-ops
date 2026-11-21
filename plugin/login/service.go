package login

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	auth "github.com/cliproxyapi-plugins/model-quality/login/account"
	codex "github.com/cliproxyapi-plugins/model-quality/login/oauth"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

type Owner struct {
	ID      string
	Valid   func() bool
	Prepare func(context.Context, string, string, string) error
}
type Options struct {
	Call            func(method string, request, response any) error
	RuntimeManifest string
	Prepare         func(context.Context, string, string, string) error
	FinishWrite     func(authID, preparationID, identity, state string) error
	PendingWrites   func() map[string]string
	DefaultProxyURL string
	Dir             string
	Config          *codex.Config
	Manager         *auth.Manager
	Owner           func() (Owner, error)
	Runtime         interface {
		Capabilities() error
		Run(context.Context, RuntimeRequest) (string, error)
	}
	// Exchange is injected only by in-process offline tests, never management input.
	Exchange func(context.Context, string, *codex.PKCECodes, string) (*codex.CodexAuthBundle, error)
	Now      func() time.Time
}
type session struct {
	knownPlans        []string
	displayEmail      string
	autoGuard         func() error
	record            storedJob
	owner             Owner
	ctx               context.Context
	cancel            context.CancelFunc
	pkce              *codex.PKCECodes
	state, url, proxy string
	workspace         string
	expected          auth.LoginSnapshot
	securityBinding   auth.LoginSnapshot
	credentials       Credentials
	remember          bool
	candidate         *auth.Auth
	consumed          bool
}
type Service struct {
	batches             accountBatches
	detailQueries       jobDetailQueries
	activeConversion    *conversionLease // guarded by mu
	accountMu           sync.Mutex
	accounts            map[string]storedAccount
	accountPermits      map[string]*atomic.Bool
	accountSettings     AccountSettings
	accountAutomation   atomic.Bool
	accountGeneration   atomic.Uint64
	loginProxy          atomic.Value
	uploadExcludedPlans atomic.Value
	mu                  sync.Mutex
	journal             *journal
	vault               *vault
	opts                Options
	jobs                map[string]*session
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	slot                chan struct{}
	closed              bool
	fault               bool
	lastPrune           time.Time
	ownsManager         bool
}

func New(opts Options) (*Service, error) {
	if opts.Config == nil {
		opts.Config = &codex.Config{ProxyURL: opts.DefaultProxyURL}
	}
	owned := opts.Manager == nil
	if owned {
		var err error
		opts.Manager, err = auth.New(opts.Dir, opts.Call)
		if err != nil {
			return nil, err
		}
	}
	initialized := false
	defer func() {
		if owned && !initialized {
			_ = opts.Manager.Close()
		}
	}()
	if opts.Owner == nil {
		opts.Owner = func() (Owner, error) {
			return Owner{ID: "plugin", Valid: opts.Manager.Valid, Prepare: func(ctx context.Context, id, identity, commitID string) error {
				if opts.Prepare != nil {
					return opts.Prepare(ctx, id, identity, commitID)
				}
				return ctx.Err()
			}}, nil
		}
	}
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	j, err := openJournal(opts.Dir)
	if err != nil {
		return nil, err
	}
	j.now = opts.Now
	if _, err = j.prune(opts.Now()); err != nil {
		_ = j.db.Close()
		return nil, errors.New("storage_unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{journal: j, opts: opts, jobs: map[string]*session{}, ctx: ctx, cancel: cancel, slot: make(chan struct{}, 1), lastPrune: opts.Now(), ownsManager: owned}
	s.vault = &vault{journal: j, manager: opts.Manager}
	if opts.Runtime == nil {
		s.opts.Runtime = NewRuntime(opts.RuntimeManifest)
	}
	if opts.Exchange == nil {
		s.opts.Exchange = func(ctx context.Context, code string, p *codex.PKCECodes, proxy string) (*codex.CodexAuthBundle, error) {
			return codex.NewCodexAuthWithProxyURL(opts.Config, proxy).ExchangeLoginCode(ctx, code, p)
		}
	}
	old, err := j.load()
	if err != nil {
		_ = j.db.Close()
		cancel()
		return nil, errors.New("storage_unavailable")
	}
	for _, v := range old {
		if !terminal(v.View.Status) || v.View.Status == "saved_not_adopted" || v.View.Status == "commit_unknown" {
			if receipt, receiptErr := opts.Manager.LoginCommitStatus(v.View.ID); receiptErr == nil {
				v.View.Status = reconcileStatus(receipt.State)
				v.View.TargetAuthID = receipt.AuthID
				v.View.CredentialEnabled = receipt.State == "committed" && receipt.CredentialEnabled
				v.AdoptedIdentity = receipt.IdentityVersion
				v.AdoptedPrincipal = receipt.Principal
				v.View.ErrorCode = ""
				if v.View.Status == "commit_unknown" || v.View.Status == "saved_not_adopted" || v.View.Status == "interrupted" {
					v.View.ErrorCode = v.View.Status
				}
			} else {
				if v.View.Status == "committing" || v.View.Status == "saved_not_adopted" || v.View.Status == "commit_unknown" {
					v.View.Status = "commit_unknown"
					v.View.ErrorCode = "commit_unknown"
				} else {
					v.View.Status = "interrupted"
					v.View.ErrorCode = "interrupted"
				}
			}
			if v.View.ErrorCode != "" && v.View.ErrorStage == "" && logStage(v.View.Phase) {
				v.View.ErrorStage = v.View.Phase
			}
			v.View.Revision++
			v.View.Phase = ""
			v.View.CandidateID = ""
			v.Updated = opts.Now()
			if err = j.save(v); err != nil {
				_ = j.db.Close()
				cancel()
				return nil, errors.New("storage_unavailable")
			}
		}
		s.jobs[v.View.ID] = &session{record: v}
	}
	if err = s.initAccounts(); err != nil {
		_ = j.db.Close()
		cancel()
		return nil, err
	}
	if err = s.recoverSecurity(); err != nil {
		_ = j.db.Close()
		cancel()
		return nil, err
	}
	if err := s.startAccountBatches(); err != nil {
		_ = j.db.Close()
		cancel()
		return nil, err
	}
	s.wg.Add(1)
	go s.monitor()
	if opts.PendingWrites != nil && opts.FinishWrite != nil {
		s.wg.Add(1)
		go s.monitorCredentialReceipts()
	}
	initialized = true
	return s, nil
}
func reconcileStatus(status string) string {
	switch status {
	case "committed", "saved_not_adopted", "commit_unknown":
		return status
	case "aborted":
		return "failed"
	case "upload_excluded":
		return "interrupted"
	default:
		return "commit_unknown"
	}
}
func terminal(v string) bool {
	switch v {
	case "succeeded", "security_uncertain", "committed", "failed", "cancelled", "discarded", "expired", "interrupted", "interaction_required", "conflict", "saved_not_adopted", "commit_unknown":
		return true
	}
	return false
}
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.cancel()
	for _, j := range s.jobs {
		if j.cancel != nil && j.record.View.Status != "committing" {
			j.cancel()
			j.candidate = nil
			j.pkce = nil
			j.state, j.url = "", ""
			j.credentials = Credentials{}
		}
	}
	s.mu.Unlock()
	// The cancelled context rejects new detail leases. Wait for any reader
	// which observed the old context to finish registering with wg.
	s.detailQueries.Lock()
	s.detailQueries.Unlock()
	s.batches.stop()
	s.wg.Wait()
	s.detailQueries.Lock()
	clear(s.detailQueries.terminal)
	s.detailQueries.Unlock()
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	clear(s.accounts)
	err := s.journal.db.Close()
	if s.ownsManager {
		err = errors.Join(err, s.opts.Manager.Close())
	}
	return err
}
func (s *Service) persistLocked(j *session, diagnostic ...LogEntry) error {
	j.record.Updated = s.opts.Now()
	if err := s.journal.save(j.record, diagnostic...); err != nil {
		s.fault = true
		s.cancel()
		return errors.New("storage_unavailable")
	}
	return nil
}
func (s *Service) transitionLocked(j *session, status, phase, code string) error {
	if j.record.View.Status == status && j.record.View.Phase == phase && j.record.View.ErrorCode == code {
		return nil
	}
	if code != "" && j.record.View.ErrorStage == "" && logStage(j.record.View.Phase) {
		j.record.View.ErrorStage = j.record.View.Phase
	}
	j.record.View.Status = status
	j.record.View.Phase = phase
	j.record.View.ErrorCode = code
	if code == "" {
		j.record.View.HTTPStatus = 0
		j.record.View.ErrorStage, j.record.View.ErrorReason, j.record.View.RuntimeCode = "", "", ""
	}
	j.record.View.Revision++
	if terminal(status) {
		s.detailQueries.remember(j)
		j.candidate = nil
		j.pkce = nil
		j.state = ""
		j.url = ""
		j.credentials = Credentials{}
		j.record.View.CandidateID = ""
	}
	return s.persistLocked(j)
}
func (s *Service) viewLocked(j *session) Job {
	v := jobPresentation(j.record)
	v.Email = j.displayEmail
	if v.Email == "" {
		v.Email = j.credentials.Email
	}
	if j.candidate != nil {
		if p, err := auth.LoginPrincipalFromAuth(j.candidate); err == nil {
			v.Email = p.Email
		}
	}
	s.completeJobEmail(&v, j.record.AdoptedPrincipal, j.expected.Principal)
	if v.Status == "waiting_browser" && j.owner.Valid != nil && j.owner.Valid() {
		v.AuthorizationURL = j.url
	}
	return v
}
func (s *Service) liveDetail(id string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.liveDetailLocked(id)
}
func (s *Service) liveDetailLocked(id string) (Job, error) {
	j := s.jobs[id]
	if j == nil || j.record.HistoryDeleted {
		return Job{}, errors.New("not_found")
	}
	if j.record.View.ID != id || !publicJobDiagnostics(j.record.View) {
		return Job{}, errors.New("storage_unavailable")
	}
	return s.viewLocked(j), nil
}
func (s *Service) Jobs() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	items, _, _ := s.jobsPageLocked("", 200)
	return items
}

// JobsPage filters account ownership before applying the bounded result limit.
func (s *Service) JobsPage(accountID string, limit int) ([]Job, bool, error) {
	if limit < 1 || limit > 200 || len(accountID) > 512 {
		return nil, false, errors.New("invalid_request")
	}
	if accountID != "" {
		s.accountMu.Lock()
		defer s.accountMu.Unlock()
		if _, ok := s.accounts[accountID]; !ok {
			return nil, false, errors.New("not_found")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.fault {
		return nil, false, errors.New("storage_unavailable")
	}
	return s.jobsPageLocked(accountID, limit)
}

func (s *Service) jobsPageLocked(accountID string, limit int) ([]Job, bool, error) {
	matched := make([]*session, 0, len(s.jobs))
	for _, j := range s.jobs {
		if !j.record.HistoryDeleted && (accountID == "" || j.record.View.AccountID == accountID) {
			matched = append(matched, j)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if matched[i].record.Created.Equal(matched[j].record.Created) {
			return matched[i].record.View.ID > matched[j].record.View.ID
		}
		return matched[i].record.Created.After(matched[j].record.Created)
	})
	more := len(matched) > limit
	if more {
		matched = matched[:limit]
	}
	out := make([]Job, 0, len(matched))
	for _, j := range matched {
		if s.jobs[j.record.View.ID] != j || !publicJobDiagnostics(j.record.View) {
			return nil, false, errors.New("storage_unavailable")
		}
		v := s.viewLocked(j)
		v.AuthorizationURL = ""
		out = append(out, v)
	}
	return out, more, nil
}
func (s *Service) Capabilities() any {
	owner, err := s.opts.Owner()
	capabilityErr := s.opts.Manager.LoginCapability()
	s.mu.Lock()
	ready := !s.closed && !s.fault
	s.mu.Unlock()
	ownerReady := ready && err == nil && owner.Valid != nil && owner.Valid()
	available := ownerReady && capabilityErr == nil
	reason := ""
	if !available {
		reason = "plugin_unavailable"
		if capabilityErr != nil {
			reason = SafeCode(capabilityErr)
		}
		if !ready {
			reason = "storage_unavailable"
		}
	}
	runtimeErr := s.opts.Runtime.Capabilities()
	securityReady := ownerReady && runtimeErr == nil && s.securityAvailable()
	result := map[string]any{"security_actions": map[string]bool{"reset_totp": securityReady, "logout_all": securityReady}, "available": available, "version": 1, "automatic_commit": true, "commit_mode": "account_auto_commit", "manual_oauth_commit": true, "reason": reason, "methods": map[string]any{"browser": map[string]any{"available": available}, "password_totp": map[string]any{"available": available && runtimeErr == nil, "reason": SafeCode(runtimeErr)}}, "vault": s.vault.status("")}
	result["account_login_available"] = ownerReady && runtimeErr == nil
	result["credential_conversion"] = available
	if metadata, ok := s.opts.Runtime.(interface {
		Metadata() (map[string]string, error)
	}); ok && runtimeErr == nil {
		if value, metadataErr := metadata.Metadata(); metadataErr == nil {
			result["runtime"] = value
		}
	}
	return result
}

// Targets preserves the original in-process list API. Management requests use
// the error-returning projection to distinguish SDK failure from an empty list.
func (s *Service) Targets() []map[string]any {
	items, err := s.targets()
	if err != nil {
		return []map[string]any{}
	}
	return items
}

func (s *Service) targets() ([]map[string]any, error) {
	targets, err := s.opts.Manager.LoginTargets()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(targets))
	for _, target := range targets {
		p := target.Principal
		out = append(out, map[string]any{"auth_id": target.ID, "auth_index": target.Index, "email": p.Email, "account_id": p.AccountID, "user_id": p.UserID, "masked_email": Mask(p.Email), "identity_version": target.Snapshot.IdentityVersion, "can_update": target.Reason == nil, "reason": SafeCode(target.Reason), "quality_paused": false, "manual_disabled": target.Disabled})
	}
	return out, nil
}

func (s *Service) Start(r StartRequest) (Job, error) {
	return s.start(r, nil)
}

// Account callers hold accountMu until admission has durably bound the job.
func (s *Service) start(r StartRequest, account *storedAccount) (Job, error) {
	manualLogin := account != nil && !r.automatic && r.securityAction == ""
	if r.securityAction == "" && !manualLogin {
		if err := s.opts.Manager.LoginCapability(); err != nil {
			return Job{}, err
		}
	}
	owner, err := s.opts.Owner()
	if err != nil || owner.Valid == nil || !owner.Valid() {
		return Job{}, errors.New("plugin_unavailable")
	}
	if r.securityAction != "" && (r.accountID == "" || !validSecurityAction(r.securityAction) || !s.securityAvailable()) {
		return Job{}, errors.New("security_unavailable")
	}
	if r.Method == "password_totp" {
		if err = s.opts.Runtime.Capabilities(); err != nil {
			return Job{}, err
		}
	}
	var expected auth.LoginSnapshot
	workspace := r.workspaceID
	proxy, _, err := s.effectiveLoginProxy(r.ProxyURL, r.TargetAuthID)
	if err != nil {
		return Job{}, err
	}
	if r.Intent == "reauthorize" {
		expected, err = s.opts.Manager.LoginSnapshot(r.TargetAuthID)
		if err != nil {
			if SafeCode(err) == "not_found" {
				return Job{}, errors.New("cpa_credential_missing")
			}
			return Job{}, err
		}
		if expected.IdentityVersion != r.ExpectedIdentityVersion {
			return Job{}, errors.New("identity_changed")
		}
		// Confirm the principal and file locator again before assigning a
		// workspace to the login engine. A failed read is not an identity.
		current, principal, plans, readErr := s.opts.Manager.LoginBinding(r.TargetAuthID)
		if readErr != nil {
			return Job{}, readErr
		}
		if current != expected {
			return Job{}, errors.New("identity_changed")
		}
		workspace = principal.AccountID
		r.knownPlans = append(r.knownPlans, plans...)
	}
	if r.UseSavedCredentials {
		r.Credentials, err = s.vault.read(r.TargetAuthID)
		if err != nil {
			return Job{}, err
		}
	}
	if r.RememberCredentials {
		if err = s.vault.ready(); err != nil {
			return Job{}, err
		}
	}
	pkce, err := codex.GeneratePKCECodes()
	if err != nil {
		return Job{}, errors.New("login_failed")
	}
	state := uuid.NewString() + uuid.NewString()
	authURL, err := codex.NewCodexAuth(s.opts.Config).GenerateAuthURL(state, pkce)
	if err != nil {
		return Job{}, errors.New("login_failed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.fault {
		return Job{}, errors.New("storage_unavailable")
	}
	now := s.opts.Now()
	removed, err := s.journal.prune(now)
	if err != nil {
		return Job{}, errors.New("storage_unavailable")
	}
	for _, id := range removed {
		delete(s.jobs, id)
	}
	s.detailQueries.forget(removed...)
	if err = s.journal.admit(); err != nil {
		return Job{}, err
	}
	key := s.journal.fingerprint(r.Credentials.Email)
	if r.TargetAuthID != "" {
		key = s.journal.fingerprint(expected.Principal)
		if a, found := s.opts.Manager.GetByID(r.TargetAuthID); found {
			if p, principalErr := auth.LoginPrincipalFromAuth(a); principalErr == nil && p.Email != "" {
				key = s.journal.fingerprint(p.Email)
			}
		}
	}
	if lease := s.activeConversion; lease != nil && lease.matches(r.Credentials.Email, workspace, expected.Principal) {
		return Job{}, errors.New("conflict")
	}
	active, attempts := 0, 0
	for _, j := range s.jobs {
		if securityBlocks(j.record) && (j.record.RateKey == key || r.TargetAuthID != "" && r.TargetAuthID == j.record.View.TargetAuthID) {
			return Job{}, errors.New("security_pending")
		}
		if !terminal(j.record.View.Status) {
			if manualLogin && j.record.View.AccountID == r.accountID && j.record.View.Status == "awaiting_commit" {
				continue
			}
			if r.securityAction != "" && j.candidate != nil {
				if p, err := auth.LoginPrincipalFromAuth(j.candidate); err == nil && s.journal.fingerprint(p.Email) == key {
					return Job{}, errors.New("conflict")
				}
			}
			active++
			if j.record.RateKey == key {
				return Job{}, errors.New("conflict")
			}
		}
		if j.record.RateKey == key && now.Sub(j.record.Created) < time.Hour {
			if manualLogin && j.record.View.AccountID == r.accountID && completedAccountAuthorization(j.record) {
				continue
			}
			attempts++
		}
	}
	if active >= 16 {
		return Job{}, errors.New("queue_full")
	}
	if attempts >= 3 {
		return Job{}, errors.New("rate_limited")
	}
	id := uuid.NewString()
	ctx, cancel := context.WithDeadline(s.ctx, now.Add(15*time.Minute))
	j := &session{knownPlans: append([]string{}, r.knownPlans...), displayEmail: r.Credentials.Email, autoGuard: r.autoGuard, record: storedJob{SecurityAccountID: workspace, AccountEvidence: r.autoEvidence, AccountSequence: r.accountSequence, View: Job{PlanType: "unknown", Action: r.securityAction, LogoutAllDevices: r.logoutAllDevices, Reauthorize: r.reauthorize, AccountID: r.accountID, Automatic: r.automatic, AutoCommit: r.autoCommit, EnableCredential: r.enableCredential, ID: id, Revision: 1, Method: r.Method, Intent: r.Intent, Status: "queued", Phase: "queued", MaskedEmail: Mask(r.Credentials.Email), TargetAuthID: r.TargetAuthID, CreatedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(15 * time.Minute).Format(time.RFC3339)}, Created: now, Updated: now, RateKey: key}, owner: owner, ctx: ctx, cancel: cancel, pkce: pkce, state: state, url: authURL, proxy: proxy, workspace: workspace, expected: expected, credentials: r.Credentials, remember: r.RememberCredentials}
	j.securityBinding = r.securityBinding
	if r.securityAction != "" {
		j.record.View.SecurityStatus = "pending"
		if _, err = s.securityJournalPath(id, true); err != nil {
			cancel()
			return Job{}, err
		}
	}
	var bound storedAccount
	if account != nil {
		bound = *account
		bound.View.LastJobID, bound.View.LastJobStatus = id, j.record.View.Status
		bound.View.SecurityPending, bound.View.LastJobAction = r.securityAction != "", r.securityAction
		bound.View.Revision++
		if !r.automatic {
			bound.View.AttentionRequired = false
			bound.View.ConsecutiveFailures = 0
			bound.ConsumedEvidence = ""
			bound.NextAttempt = time.Time{}
		}
	}
	// The command-to-job link, job and optional account binding share the
	// admission transaction. The worker cannot run ahead of this checkpoint.
	err = s.journal.db.Update(func(tx *bolt.Tx) error {
		if err := s.journal.saveTx(tx, j.record); err != nil {
			return err
		}
		if r.commandID != "" {
			if err := bindJobCommand(tx, r.commandID, id); err != nil {
				return err
			}
		}
		if account != nil {
			return putGob(tx, "accounts", bound.View.ID, bound)
		}
		return nil
	})
	if err != nil {
		s.fault = true
		s.cancel()
		err = errors.New("storage_unavailable")
	} else if account != nil {
		s.accounts[bound.View.ID] = bound
	}
	if err != nil {
		cancel()
		return Job{}, err
	}
	s.jobs[id] = j
	if r.Method == "browser" {
		err = s.transitionLocked(j, "waiting_browser", "authorization", "")
	} else {
		s.wg.Add(1)
		go s.run(j)
	}
	return s.viewLocked(j), err
}
func completedAccountAuthorization(j storedJob) bool {
	if j.AuthorizationSavedAt != "" {
		return true
	}
	switch j.View.Status {
	case "committed", "succeeded":
		return true
	case "discarded", "expired":
		return j.PlanObserved
	}
	return false
}

// Only the most recent verified grant is retained per account. Replace it and
// retire the preceding upload candidate together, after token exchange succeeds.
func (s *Service) saveAccountAuthorization(j *session) error {
	id := j.record.View.AccountID
	raw, err := Encode(j.candidate.Metadata)
	if err != nil {
		return errors.New("storage_unavailable")
	}
	now := s.opts.Now()
	record := j.record
	record.AuthorizationSavedAt = now.Format(time.RFC3339Nano)
	var previous *session
	var discarded storedJob
	for _, other := range s.jobs {
		if other != j && other.record.View.AccountID == id && other.record.View.Status == "awaiting_commit" {
			previous = other
			discarded = other.record
			discarded.View.Status, discarded.View.Phase, discarded.View.CandidateID = "discarded", "", ""
			discarded.View.ErrorCode, discarded.View.ErrorStage = "", ""
			discarded.View.Revision++
			discarded.Updated = now
			break
		}
	}
	if previous != nil {
		s.detailQueries.remember(previous)
	}
	if err := s.journal.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte("account-authorizations"))
		if err != nil {
			return err
		}
		if err := b.Put([]byte(id), raw); err != nil {
			return err
		}
		if previous != nil {
			if err := s.journal.saveTx(tx, discarded); err != nil {
				return err
			}
		}
		return s.journal.saveTx(tx, record)
	}); err != nil {
		return errors.New("storage_unavailable")
	}
	j.record = record
	if previous != nil {
		previous.cancel()
		previous.record, previous.candidate, previous.credentials = discarded, nil, Credentials{}
	}
	return nil
}
func (s *Service) checkAutomaticExecution(j *session) error {
	if j.autoGuard != nil {
		if err := j.autoGuard(); err != nil {
			return err
		}
	}
	if j.record.View.Automatic {
		// Queue admission may precede a pause, cooldown, or replacement grant.
		// Recheck frozen evidence before submitting credentials or an OAuth code.
		health, err := s.opts.Manager.LoginHealth(j.expected.AuthID)
		if err != nil || !health.AutoEligible || health.Fingerprint != j.record.AccountEvidence ||
			health.IdentityVersion != j.expected.IdentityVersion || health.Principal != j.expected.Principal {
			return errors.New("policy_changed")
		}
	}
	return nil
}
func (s *Service) run(j *session) {
	defer s.wg.Done()
	securityReconciled := false
	if j.record.View.Action != "" {
		defer func() { s.finishSecurityRun(j, !securityReconciled) }()
	}
	select {
	case s.slot <- struct{}{}:
		defer func() { <-s.slot }()
	case <-j.ctx.Done():
		s.finishError(j, j.ctx.Err())
		return
	}
	if err := s.checkAutomaticExecution(j); err != nil {
		s.finishError(j, err)
		return
	}
	s.mu.Lock()
	if terminal(j.record.View.Status) || j.ctx.Err() != nil {
		s.mu.Unlock()
		s.finishError(j, errors.New("cancelled"))
		return
	}
	if err := s.transitionLocked(j, "running", "authorization", ""); err != nil {
		s.mu.Unlock()
		return
	}
	req := RuntimeRequest{Operation: j.record.View.Action, LogoutAfterReset: j.record.View.LogoutAllDevices, Reauthorize: j.record.View.Reauthorize, Version: 1, JobID: j.record.View.ID, Attempt: 1, AuthorizationURL: j.url, RedirectURI: codex.RedirectURI, State: j.state, Credentials: j.credentials, ProxyURL: j.proxy, WorkspaceID: j.workspace}
	if req.Operation != "" {
		req.SecurityJournalPath, _ = s.securityJournalPath(j.record.View.ID, false)
		j.record.SecurityStarted = true
		j.record.View.SecurityStatus = "running"
		if err := s.persistLocked(j); err != nil {
			s.mu.Unlock()
			return
		}
	}
	initialCredentials := req.Credentials
	req.commitSecurity = func() error { return s.commitSecuritySeed(j) }
	req.factorVerified = func(id string) error { return s.bindVerifiedFactor(j, initialCredentials, id) }
	if req.ProxyURL == "direct" {
		req.ProxyURL = ""
	}
	if !j.remember {
		j.credentials = Credentials{}
	}
	s.mu.Unlock()
	var callback string
	var err error
	if detailed, ok := s.opts.Runtime.(interface {
		RunWithDiagnostics(context.Context, RuntimeRequest, func(string) error, func(LogEntry) error) (string, error)
	}); ok {
		callback, err = detailed.RunWithDiagnostics(j.ctx, req, func(phase string) error { return s.runtimeProgress(j, phase) }, func(entry LogEntry) error { return s.runtimeDiagnostic(j, entry) })
	} else if progressive, ok := s.opts.Runtime.(interface {
		RunWithProgress(context.Context, RuntimeRequest, func(string) error) (string, error)
	}); ok {
		callback, err = progressive.RunWithProgress(j.ctx, req, func(phase string) error { return s.runtimeProgress(j, phase) })
	} else {
		callback, err = s.opts.Runtime.Run(j.ctx, req)
	}
	req.Credentials = Credentials{}
	if req.Operation != "" {
		if err != nil {
			s.mu.Lock()
			// Reconciliation may quarantine an uncertain mutation before the
			// terminal error handler runs. Preserve its runtime diagnosis first.
			if !terminal(j.record.View.Status) {
				j.record.View.ErrorStage = j.record.View.Phase
				var diagnostic *runtimeDiagnosticError
				if errors.As(err, &diagnostic) {
					if diagnostic.stage != "" {
						j.record.View.ErrorStage = diagnostic.stage
					}
					j.record.View.ErrorReason, j.record.View.RuntimeCode = diagnostic.reason, diagnostic.runtimeCode
				}
			}
			s.mu.Unlock()
		}
		securityReconciled = true
		if reconcileErr := s.reconcileSecurity(j); reconcileErr != nil && (err == nil || reconcileErr.Error() != "security_failed") {
			s.finishError(j, reconcileErr)
			return
		}
	}
	if err != nil {
		s.finishError(j, err)
		return
	}
	if req.Operation != "" && !req.Reauthorize {
		s.mu.Lock()
		if !terminal(j.record.View.Status) && j.ctx.Err() == nil {
			_ = s.transitionLocked(j, "succeeded", "security_complete", "")
		}
		s.mu.Unlock()
		return
	}
	if _, err = s.Callback(j.record.View.ID, callback); err != nil {
		s.finishError(j, err)
	}
}

func (s *Service) runtimeProgress(j *session, phase string) error {
	if !runtimeStage(phase) {
		return runtimeError("runtime_protocol_error")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := j.ctx.Err(); err != nil {
		return err
	}
	if s.closed || s.fault || s.jobs[j.record.View.ID] != j || j.record.View.Status != "running" || !j.owner.Valid() {
		return runtimeError("cancelled")
	}
	return s.transitionLocked(j, "running", phase, "")
}

func (s *Service) finishError(j *session, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if terminal(j.record.View.Status) || j.record.View.Status == "committing" {
		return
	}
	code := SafeCode(err)
	status := "failed"
	if errors.Is(err, context.Canceled) || j.record.View.Status == "cancelling" {
		status = "cancelled"
		code = "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		status = "expired"
		code = "expired"
	}
	if code == "interaction_required" {
		status = code
	}
	j.record.View.ErrorStage, j.record.View.ErrorReason, j.record.View.RuntimeCode = "", "", ""
	j.record.View.HTTPStatus = 0
	var exchangeError *codex.ExchangeError
	if errors.As(err, &exchangeError) && status != "cancelled" && status != "expired" {
		if exchangeError.HTTPStatus >= 100 && exchangeError.HTTPStatus <= 599 {
			j.record.View.HTTPStatus = exchangeError.HTTPStatus
		}
		if runtimeReason(exchangeError.Reason) {
			j.record.View.ErrorReason = exchangeError.Reason
		}
	}
	var diagnostic *runtimeDiagnosticError
	if errors.As(err, &diagnostic) && status != "cancelled" && status != "expired" {
		j.record.View.ErrorStage, j.record.View.ErrorReason, j.record.View.RuntimeCode = diagnostic.stage, diagnostic.reason, diagnostic.runtimeCode
		if diagnostic.runtimeCode == "service_error" {
			if logs, readErr := s.journal.logs(j.record.View.ID); readErr == nil {
				j.record.View.HTTPStatus = serviceHTTPStatus(logs.Entries, diagnostic.stage)
			}
		}
	}
	_ = s.transitionLocked(j, status, "", code)
	if j.cancel != nil {
		j.cancel()
	}
}
func (s *Service) Callback(id, callback string) (Job, error) {
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil || j.record.HistoryDeleted {
		s.mu.Unlock()
		return Job{}, errors.New("not_found")
	}
	if j.consumed || (j.record.View.Status != "waiting_browser" && j.record.View.Status != "running") {
		s.mu.Unlock()
		return Job{}, errors.New("already_consumed")
	}
	if j.ctx.Err() != nil || !j.owner.Valid() {
		s.mu.Unlock()
		return Job{}, errors.New("cancelled")
	}
	code, err := validateCallback(callback, j.state)
	if err != nil {
		s.mu.Unlock()
		return Job{}, err
	}
	j.consumed = true
	pkce := j.pkce
	j.pkce = nil
	if err = s.transitionLocked(j, "exchanging", "token_exchange", ""); err != nil {
		s.mu.Unlock()
		return Job{}, err
	}
	s.wg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.wg.Done()
		select {
		case s.slot <- struct{}{}:
			defer func() { <-s.slot }()
		case <-j.ctx.Done():
			s.finishError(j, j.ctx.Err())
			return
		}
		if err := j.ctx.Err(); err != nil {
			s.finishError(j, err)
			return
		}
		if err := s.checkAutomaticExecution(j); err != nil {
			s.finishError(j, err)
			return
		}
		ctx, cancel := context.WithTimeout(j.ctx, 60*time.Second)
		defer cancel()
		bundle, err := s.opts.Exchange(ctx, code, pkce, j.proxy)
		if err != nil {
			s.finishError(j, err)
			return
		}
		candidate, err := validateBundle(bundle, s.opts.Now())
		if err != nil {
			s.finishError(j, err)
			return
		}
		p, _ := auth.LoginPrincipalFromAuth(candidate)
		s.mu.Lock()
		defer s.mu.Unlock()
		if j.ctx.Err() != nil || !j.owner.Valid() || j.record.View.Status != "exchanging" {
			if !terminal(j.record.View.Status) && j.record.View.Status != "committing" {
				status := "cancelled"
				if errors.Is(j.ctx.Err(), context.DeadlineExceeded) {
					status = "expired"
				}
				_ = s.transitionLocked(j, status, "", status)
			}
			return
		}
		for _, other := range s.jobs {
			if other != j && securityBlocks(other.record) && other.record.RateKey == s.journal.fingerprint(p.Email) {
				_ = s.transitionLocked(j, "conflict", "", "security_pending")
				return
			}
		}
		if j.record.View.AccountID != "" && s.journal.fingerprint(p.Email) != j.record.RateKey {
			_ = s.transitionLocked(j, "conflict", "", "identity_conflict")
			return
		}
		if j.expected.AuthID != "" && p.Key() != j.expected.Principal {
			_ = s.transitionLocked(j, "conflict", "", "identity_conflict")
			return
		}
		manualLogin := j.record.View.AccountID != "" && j.record.View.Action == "" && !j.record.View.Automatic
		if manualLogin && j.securityBinding.Principal != "" && p.Key() != j.securityBinding.Principal {
			_ = s.transitionLocked(j, "conflict", "", "identity_conflict")
			return
		}
		if j.record.View.Action != "" && (!validSecurityBinding(j.record.SecurityAccountID) || p.AccountID != j.record.SecurityAccountID) {
			_ = s.transitionLocked(j, "conflict", "", "identity_conflict")
			return
		}
		if !manualLogin {
			if err := s.prepareGrantDestination(j, p); err != nil {
				j.record.View.ErrorStage = "saving"
				code := SafeCode(err)
				status := "failed"
				if code == "identity_conflict" || code == "identity_changed" {
					status = "conflict"
				}
				_ = s.transitionLocked(j, status, "", code)
				return
			}
		}
		if j.expected.AuthID != "" {
			candidate.ID = j.expected.AuthID
		} else {
			candidate.ID = uuid.NewString() + ".json"
		}
		candidate.FileName = candidate.ID
		candidate.ProxyURL = j.proxy
		j.candidate = candidate
		j.record.View.PlanType = auth.LoginPlanType(candidate)
		j.record.PlanObserved = true
		j.displayEmail = p.Email
		j.record.View.MaskedEmail = Mask(p.Email)
		j.record.View.ExpiresAt = s.opts.Now().Add(5 * time.Minute).Format(time.RFC3339)
		if manualLogin {
			if err := s.saveAccountAuthorization(j); err != nil {
				_ = s.transitionLocked(j, "failed", "", SafeCode(err))
				return
			}
		}
		if j.record.View.AutoCommit {
			if err := s.beginCommitLocked(j); err != nil {
				j.record.View.ErrorStage = "saving"
				j.record.View.CandidateID = id + ":candidate"
				_ = s.transitionLocked(j, "awaiting_commit", "review", SafeCode(err))
			}
		} else {
			j.record.View.CandidateID = id + ":candidate"
			_ = s.transitionLocked(j, "awaiting_commit", "review", "")
		}
	}()
	return s.liveDetail(id)
}
func (s *Service) prepareGrantDestination(j *session, p auth.LoginPrincipal) error {
	if err := s.resolveSecurityCredential(j, p); err != nil {
		return err
	}
	existing, err := s.opts.Manager.ListChecked()
	if err != nil {
		return err
	}
	for _, a := range existing {
		other, err := auth.LoginPrincipalFromAuth(a)
		if err == nil && other.Key() == p.Key() && a.ID != j.expected.AuthID {
			return errors.New("duplicate_identity")
		}
	}
	return nil
}
func (s *Service) Candidate(id string) (Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobID := strings.TrimSuffix(id, ":candidate")
	j := s.jobs[jobID]
	if j == nil || j.record.View.CandidateID != id || j.candidate == nil || j.record.View.Status != "awaiting_commit" {
		return Candidate{}, errors.New("not_found")
	}
	p, err := auth.LoginPrincipalFromAuth(j.candidate)
	if err != nil {
		return Candidate{}, err
	}
	restrictions := []string{"not_inference_verified", "credential_write_isolation"}
	if j.expected.AuthID == "" {
		restrictions = append(restrictions, "new_credential_disabled")
	} else {
		t, e := s.opts.Manager.QualityTarget(j.expected.AuthID)
		if e != nil {
			return Candidate{}, e
		}
		restrictions = append(restrictions, t.UnavailableReasons...)
	}
	if err := s.checkUploadPlans(j.candidate, j.expected, j.knownPlans); err != nil && err.Error() == "cpa_plan_excluded" {
		restrictions = append(restrictions, "cpa_plan_excluded")
	}
	return Candidate{PlanType: auth.LoginPlanType(j.candidate), ID: id, JobID: jobID, Revision: j.record.View.Revision, Email: p.Email, AccountID: p.AccountID, UserID: p.UserID, MaskedEmail: Mask(p.Email), AccountIDMasked: Mask(p.AccountID), TargetAuthID: j.candidate.ID, Restrictions: restrictions, Intent: j.record.View.Intent}, nil
}
func (s *Service) Cancel(id string, discard bool) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil || j.record.HistoryDeleted {
		return Job{}, errors.New("not_found")
	}
	if terminal(j.record.View.Status) || j.record.View.Status == "committing" {
		return s.viewLocked(j), nil
	}
	if j.cancel != nil {
		j.cancel()
	}
	status := "cancelled"
	if discard {
		status = "discarded"
	}
	if j.record.View.Status == "running" || j.record.View.Status == "exchanging" || j.record.View.Status == "cancelling" {
		status = "cancelling"
	}
	err := s.transitionLocked(j, status, "", "")
	return s.viewLocked(j), err
}
func (s *Service) Commit(ctx context.Context, id, candidateID string, revision uint64) (Job, error) {
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil || j.record.HistoryDeleted {
		s.mu.Unlock()
		return Job{}, errors.New("not_found")
	}
	if j.record.View.Status == "committed" || j.record.View.Status == "committing" {
		v := s.viewLocked(j)
		s.mu.Unlock()
		return v, nil
	}
	if j.record.View.Status != "awaiting_commit" || j.record.View.Revision != revision || j.record.View.CandidateID != candidateID || j.candidate == nil {
		s.mu.Unlock()
		return Job{}, errors.New("conflict")
	}
	if j.ctx.Err() != nil || !j.owner.Valid() {
		s.mu.Unlock()
		return Job{}, errors.New("cancelled")
	}
	expiry, errExpiry := time.Parse(time.RFC3339, j.record.View.ExpiresAt)
	if errExpiry != nil || !s.opts.Now().Before(expiry) {
		_ = s.transitionLocked(j, "expired", "", "expired")
		j.cancel()
		s.mu.Unlock()
		return Job{}, errors.New("expired")
	}
	err := s.beginCommitLocked(j)
	s.mu.Unlock()
	if err != nil {
		return Job{}, err
	}
	return s.liveDetail(id)
}

// beginCommitLocked records submission before starting the only credential write.
// Account login is authorized when the operator starts it; explicit OAuth callers
// still enter through Commit with their reviewed candidate and revision.
func (s *Service) beginCommitLocked(j *session) error {
	if j.candidate == nil {
		return errors.New("conflict")
	}
	principal, err := auth.LoginPrincipalFromAuth(j.candidate)
	if err != nil {
		return err
	}
	nextSequence := j.record.AccountSequence
	for _, other := range s.jobs {
		if other != j && j.record.View.AccountID != "" && other.record.View.AccountID == j.record.View.AccountID && !terminal(other.record.View.Status) && other.record.View.Status != "awaiting_commit" {
			return errors.New("conflict")
		}
		if j.record.View.AccountID != "" && other.record.View.AccountID == j.record.View.AccountID && newerAccountJob(&other.record, &j.record) {
			if other.record.View.Status == "saved_not_adopted" || other.record.View.Status == "commit_unknown" {
				return errors.New("conflict")
			}
			if other.record.AccountSequence >= nextSequence {
				nextSequence = other.record.AccountSequence + 1
			}
		}
		if other != j && securityBlocks(other.record) && other.record.RateKey == s.journal.fingerprint(principal.Email) {
			return errors.New("security_pending")
		}
	}
	if j.record.View.AccountID != "" && j.record.View.Action == "" && !j.record.View.Automatic {
		if err := s.prepareGrantDestination(j, principal); err != nil {
			return err
		}
		if j.expected.AuthID != "" {
			j.candidate.ID, j.candidate.FileName = j.expected.AuthID, j.expected.Name
		}
	}
	candidate := j.candidate.Clone()
	expected := j.expected
	previous := j.securityBinding
	replacing := previous.AuthID != "" && expected.AuthID == ""
	knownPlans := append([]string{}, j.knownPlans...)
	owner := j.owner
	id := j.record.View.ID
	enableCredential := j.record.View.EnableCredential
	accountID := j.record.View.AccountID
	if err := s.checkUploadPlans(candidate, expected, knownPlans); err != nil && err.Error() == "cpa_plan_excluded" {
		j.record.View.CandidateID = id + ":candidate"
		return s.transitionLocked(j, "awaiting_commit", "review", "cpa_plan_excluded")
	}
	// A manual upload after a later failed login is the newest account action.
	j.record.AccountSequence = nextSequence
	if err := s.transitionLocked(j, "committing", "saving", ""); err != nil {
		return err
	}
	s.wg.Add(1)
	// The commit is detached from an HTTP disconnect after its gate is durable.
	go func() {
		defer s.wg.Done()
		commitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		guard := func() error {
			if err := commitCtx.Err(); err != nil {
				return errors.New("cancelled")
			}
			if err := j.ctx.Err(); err != nil {
				return errors.New("cancelled")
			}
			if !owner.Valid() {
				return errors.New("plugin_unavailable")
			}
			if err := s.checkUploadPlans(candidate, expected, knownPlans); err != nil {
				return err
			}
			if replacing {
				_, err := s.opts.Manager.LoginSnapshot(previous.AuthID)
				if err == nil {
					return errors.New("policy_changed")
				}
				if SafeCode(err) != "not_found" {
					return err
				}
			}
			return s.checkAutomaticExecution(j)
		}
		prepared := make(map[string]string)
		result, err := s.opts.Manager.CommitLoginCandidate(commitCtx, auth.LoginCommitRequest{CommitID: id, Candidate: candidate, Expected: expected, EnableCredential: enableCredential, Guard: guard, Prepare: func(ctx context.Context, authID, identity string) error {
			if replacing {
				previousID := previousBindingPrefix + id
				prepared[previous.AuthID] = previousID
				if err := owner.Prepare(ctx, previous.AuthID, previous.IdentityVersion, previousID); err != nil {
					return err
				}
			}
			prepared[authID] = id
			return owner.Prepare(ctx, authID, identity, id)
		}})
		for authID, preparationID := range prepared {
			if finishErr := s.finishCredentialWrite(authID, preparationID, result); finishErr != nil && err == nil {
				err = errors.New("quality_reconciliation_pending")
			}
		}
		status := result.State
		excluded := err != nil && err.Error() == "cpa_plan_excluded" && (status == "aborted" || status == "upload_excluded")
		if status == "" {
			status = "failed"
		}
		if status != "committed" && status != "saved_not_adopted" && status != "commit_unknown" {
			status = "failed"
		}
		s.mu.Lock()
		if excluded {
			j.record.View.CandidateID = id + ":candidate"
			_ = s.transitionLocked(j, "awaiting_commit", "review", "cpa_plan_excluded")
			s.mu.Unlock()
			if accountID != "" {
				s.accountMu.Lock()
				_ = s.syncAccountJobsLocked()
				s.accountMu.Unlock()
			}
			return
		}
		if status == "committed" && j.remember {
			if e := s.vault.save(result.AuthID, 0, j.credentials, true); e != nil {
				j.record.View.VaultError = SafeCode(e)
			}
		}
		j.record.AdoptedIdentity = result.IdentityVersion
		j.record.AdoptedPrincipal = result.Principal
		j.record.View.TargetAuthID = result.AuthID
		j.record.View.CredentialEnabled = status == "committed" && result.CredentialEnabled
		_ = s.transitionLocked(j, status, "", SafeCode(err))
		if j.cancel != nil {
			j.cancel()
		}
		s.mu.Unlock()
		if accountID != "" {
			s.accountMu.Lock()
			_ = s.syncAccountJobsLocked()
			s.accountMu.Unlock()
		}
	}()
	return nil
}
func (s *Service) monitor() {
	defer s.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.maintain()
			s.maintainAccounts()
		}
	}
}
func (s *Service) maintain() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.fault {
		return
	}
	now := s.opts.Now()
	for _, j := range s.jobs {
		if terminal(j.record.View.Status) || j.record.View.Status == "committing" || j.record.View.Status == "cancelling" || j.cancel == nil {
			continue
		}
		expiry, _ := time.Parse(time.RFC3339, j.record.View.ExpiresAt)
		if !now.Before(expiry) || j.ctx.Err() != nil || !j.owner.Valid() {
			j.cancel()
			if j.record.View.Status != "running" && j.record.View.Status != "exchanging" {
				_ = s.transitionLocked(j, "expired", "", "expired")
			} else {
				_ = s.transitionLocked(j, "cancelling", "", "")
			}
		}
	}
	if !now.Before(s.lastPrune.Add(time.Hour)) {
		removed, err := s.journal.prune(now)
		if err != nil {
			s.fault = true
			s.cancel()
			return
		}
		for _, id := range removed {
			delete(s.jobs, id)
		}
		s.detailQueries.forget(removed...)
		s.lastPrune = now
	}
}
func (s *Service) Idempotent(key, path string, body []byte, fn func() (any, error)) ([]byte, error) {
	raw, err := s.journal.idempotent(key, path, body, fn)
	if err != nil {
		return nil, err
	}
	// The journal excludes display identities. Reconstruct them for both first
	// responses and replay without persisting plaintext account material in idempotency records.
	d := ojson.NewDoc(raw)
	value := ojson.ParseBytes(raw)
	if strings.Contains(path, "/accounts/") {
		if items := value.Get("items"); items.IsArray() {
			for i, account := range items.Array() {
				_ = d.Set(fmt.Sprintf("items.%d.email", i), s.storedAccountEmail(account.Get("id").String()))
			}
		} else if value.Get("health_status").Exists() {
			_ = d.Set("email", s.storedAccountEmail(value.Get("id").String()))
		}
	}
	if value.Get("method").Exists() {
		id := value.Get("id").String()
		if _, err := decodeJobReply(raw, id); err != nil {
			return nil, err
		}
		s.mu.Lock()
		deleted := s.jobs[id] != nil && s.jobs[id].record.HistoryDeleted
		s.mu.Unlock()
		if deleted {
			return nil, errors.New("not_found")
		}
		if job, err := s.liveDetail(id); err == nil {
			_ = d.Set("email", job.Email)
		}
	}
	if value.Get("entry").Exists() {
		if c, err := s.vault.read(ojson.ParseBytes(body).Get("auth_id").String()); err == nil {
			_ = d.Set("entry.email", c.Email)
		}
	}
	return d.Bytes(), nil
}
func validateBundle(bundle *codex.CodexAuthBundle, now time.Time) (*auth.Auth, error) {
	if bundle == nil || bundle.TokenData.AccessToken == "" || bundle.TokenData.RefreshToken == "" || bundle.TokenData.IDToken == "" {
		return nil, errors.New("identity_unverifiable")
	}
	t := bundle.TokenData
	parts := strings.Split(t.IDToken, ".")
	if len(parts) != 3 {
		return nil, errors.New("identity_unverifiable")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !ojson.ValidBytes(raw) {
		return nil, errors.New("identity_unverifiable")
	}
	claims := ojson.ParseBytes(raw)
	aud := claims.Get("aud")
	audOK := aud.String() == codex.ClientID
	for _, v := range aud.Array() {
		if v.String() == codex.ClientID {
			audOK = true
		}
	}
	if !audOK || claims.Get("iss").String() != "https://auth.openai.com" || claims.Get("exp").Int() <= now.Unix() {
		return nil, errors.New("identity_unverifiable")
	}
	a := &auth.Auth{Provider: "codex", Metadata: map[string]any{"type": "codex", "id_token": t.IDToken, "access_token": t.AccessToken, "refresh_token": t.RefreshToken, "expired": t.Expire, "last_refresh": bundle.LastRefresh}}
	p, err := auth.LoginPrincipalFromAuth(a)
	if err != nil {
		return nil, err
	}
	a.Metadata["account_id"] = p.AccountID
	a.Metadata["email"] = p.Email
	a.Metadata["plan_type"] = auth.LoginPlanType(a)
	return a, nil
}
