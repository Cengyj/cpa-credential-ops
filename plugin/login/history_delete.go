package login

import (
	"errors"

	bolt "go.etcd.io/bbolt"
)

func canDeleteJob(record storedJob) bool {
	if record.HistoryDeleted || !terminal(record.View.Status) || securityBlocks(record) {
		return false
	}
	switch record.View.Status {
	case "commit_unknown", "saved_not_adopted", "security_uncertain":
		return false
	default:
		return true
	}
}

func (s *Service) DeleteJob(id string, revision uint64) error {
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	if err := s.accountReady(); err != nil {
		return err
	}
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil || j.record.HistoryDeleted {
		s.mu.Unlock()
		return errors.New("not_found")
	}
	allowed := canDeleteJob(j.record) && j.record.View.Revision == revision
	s.mu.Unlock()
	if !allowed {
		return errors.New("conflict")
	}
	// Apply the completed job's account effects before removing its result.
	if err := s.syncAccountJobsLocked(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.fault {
		return errors.New("storage_unavailable")
	}
	j = s.jobs[id]
	if j == nil || j.record.HistoryDeleted {
		return errors.New("not_found")
	}
	v := j.record.View
	if !canDeleteJob(j.record) || v.Revision != revision {
		return errors.New("conflict")
	}
	// Keep the account's ordering checkpoint and rate-limit evidence until
	// retention prunes this record. The history content and log events are dropped.
	deleted := storedJob{
		HistoryDeleted:  true,
		PlanObserved:    j.record.PlanObserved,
		AccountSequence: j.record.AccountSequence,
		Created:         j.record.Created,
		Updated:         s.opts.Now(),
		RateKey:         j.record.RateKey,
		View:            Job{ID: id, AccountID: v.AccountID, Status: v.Status},
	}
	if err := s.journal.db.Update(func(tx *bolt.Tx) error {
		return putGob(tx, "jobs", id, deleted)
	}); err != nil {
		return errors.New("storage_unavailable")
	}
	s.jobs[id] = &session{record: deleted}
	s.detailQueries.forget(id)
	return nil
}
