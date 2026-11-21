package login

import (
	"bytes"
	"encoding/gob"
	"errors"
	"sync"

	auth "github.com/cliproxyapi-plugins/model-quality/login/account"
	bolt "go.etcd.io/bbolt"
)

// These are the only terminal display inputs that cannot be restored from the
// journal. They never contain an OAuth URL or credentials, and convey no authority.
type jobDisplayIdentity struct {
	email, expectedPrincipal string
}

type jobDetailQueries struct {
	sync.RWMutex
	terminal map[string]jobDisplayIdentity
}

// Publish before the terminal journal transaction. A reader of an earlier
// nonterminal record still requires the live session and owner validation.
func (q *jobDetailQueries) remember(j *session) {
	identity := jobDisplayIdentity{j.displayEmail, j.expected.Principal}
	if identity == (jobDisplayIdentity{}) {
		return
	}
	q.Lock()
	defer q.Unlock()
	if q.terminal == nil {
		q.terminal = make(map[string]jobDisplayIdentity)
	}
	q.terminal[j.record.View.ID] = identity
}

func (q *jobDetailQueries) forget(ids ...string) {
	if len(ids) == 0 {
		return
	}
	q.Lock()
	defer q.Unlock()
	for _, id := range ids {
		delete(q.terminal, id)
	}
}

// Read identity and deletion from the same durable row. Older diagnostic
// records may have a log window without a complete public job state.
func (j *journal) visibleJob(id string) (storedJob, error) {
	var record storedJob
	var found bool
	err := j.db.View(func(tx *bolt.Tx) error {
		jobs := tx.Bucket([]byte("jobs"))
		if jobs == nil {
			return errors.New("missing jobs bucket")
		}
		raw := jobs.Get([]byte(id))
		if raw == nil {
			return nil
		}
		found = true
		return gob.NewDecoder(bytes.NewReader(raw)).Decode(&record)
	})
	if err != nil || found && record.View.ID != id {
		return storedJob{}, errors.New("storage_unavailable")
	}
	if !found || record.HistoryDeleted {
		return storedJob{}, errors.New("not_found")
	}
	return record, nil
}

func (j *journal) detail(id string) (storedJob, error) {
	record, err := j.visibleJob(id)
	if err == nil && !publicJobDiagnostics(record.View) {
		return storedJob{}, errors.New("storage_unavailable")
	}
	return record, err
}

func jobPresentation(record storedJob) Job {
	v := record.View
	v.Email, v.AuthorizationURL = "", ""
	if v.Status == "failed" && v.ErrorCode == "runtime_failed" && v.RuntimeCode == "service_error" && v.HTTPStatus == 0 {
		v.HTTPStatus = serviceHTTPStatus(record.LogEntries, v.ErrorStage)
	}
	v.PlanType, _ = auth.ParsePlan(v.PlanType)
	v.CanDelete = canDeleteJob(record)
	return v
}

func (s *Service) completeJobEmail(v *Job, principal, expected string) {
	if v.Email == "" && v.AccountID != "" {
		v.Email = s.storedAccountEmail(v.AccountID)
	}
	if v.Email == "" && v.TargetAuthID != "" {
		if principal == "" {
			principal = expected
		}
		if a, ok := s.opts.Manager.GetByID(v.TargetAuthID); ok {
			if p, err := auth.LoginPrincipalFromAuth(a); err == nil && principal != "" && p.Key() == principal {
				v.Email = p.Email
			}
		}
	}
}

func (s *Service) readLiveDetail(id string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A transition can mutate the session and then fail its durable write
	// while this previously admitted reader waits for the lock.
	if s.closed || s.fault || s.ctx.Err() != nil {
		return Job{}, errors.New("storage_unavailable")
	}
	return s.liveDetailLocked(id)
}

func (s *Service) Detail(id string) (Job, error) {
	q := &s.detailQueries
	q.RLock()
	if s.ctx.Err() != nil {
		q.RUnlock()
		return Job{}, errors.New("storage_unavailable")
	}
	s.wg.Add(1)
	defer s.wg.Done()
	record, err := s.journal.detail(id)
	identity := q.terminal[id]
	q.RUnlock()
	if err != nil {
		return Job{}, err
	}
	if !terminal(record.View.Status) {
		return s.readLiveDetail(id)
	}
	v := jobPresentation(record)
	v.Email = identity.email
	if v.Email != "" || v.AccountID == "" && v.TargetAuthID == "" {
		return v, nil
	}
	s.completeJobEmail(&v, record.AdoptedPrincipal, identity.expectedPrincipal)
	// A deletion or a changed binding can win while the independent SDK read
	// is in flight. Recheck before returning its resolved identity.
	current, err := s.journal.detail(id)
	if err != nil {
		return Job{}, err
	}
	if record.View != current.View || record.AdoptedPrincipal != current.AdoptedPrincipal ||
		record.SecurityResolved != current.SecurityResolved {
		return s.readLiveDetail(id)
	}
	return v, nil
}
