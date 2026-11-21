package login

import (
	"bytes"
	"encoding/gob"
	"errors"
	"strings"

	auth "github.com/cliproxyapi-plugins/model-quality/login/account"
	bolt "go.etcd.io/bbolt"
)

func jobCommandPath(path string) string {
	switch path {
	case "POST/start", "POST/accounts/start", "POST/accounts/security":
		return strings.TrimPrefix(path, "POST")
	}
	return ""
}

func bindJobCommand(tx *bolt.Tx, commandID, jobID string) error {
	var record idempotency
	raw := tx.Bucket([]byte("idempotency")).Get([]byte(commandID))
	if raw == nil || gob.NewDecoder(bytes.NewReader(raw)).Decode(&record) != nil || !record.Pending || record.JobPath == "" || record.JobID != "" {
		return errors.New("storage_unavailable")
	}
	record.JobID = jobID
	return putGob(tx, "idempotency", commandID, record)
}

type jobCommandReceipt struct {
	ID             string `json:"id"`
	Path           string `json:"path,omitempty"`
	Status         string `json:"status"`
	JobID          string `json:"job_id,omitempty"`
	Job            *Job   `json:"job,omitempty"`
	Code           string `json:"code,omitempty"`
	OutcomeUnknown bool   `json:"outcome_unknown,omitempty"`
}

// Decode only the command link and public job state. Historical HTTP response
// bodies, credential metadata and per-job diagnostic entries are not needed.
type commandLink struct {
	Fingerprint, JobPath, JobID, Code string
	Pending, Retired                  bool
}

type commandJobState struct {
	HistoryDeleted bool
	View           Job
}

func (j *journal) readJobCommand(id string) (*commandLink, *commandJobState, error) {
	var link *commandLink
	var job *commandJobState
	err := j.db.View(func(tx *bolt.Tx) error {
		keys, jobs := tx.Bucket([]byte("idempotency")), tx.Bucket([]byte("jobs"))
		if keys == nil || jobs == nil {
			return errors.New("missing command journal bucket")
		}
		raw := keys.Get([]byte(id))
		if raw == nil {
			return nil
		}
		link = new(commandLink)
		if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(link); err != nil {
			return err
		}
		if link.Fingerprint == "" || link.JobPath != "" && jobCommandPath("POST"+link.JobPath) == "" {
			return errors.New("invalid command link")
		}
		if link.Retired || link.JobID == "" {
			return nil
		}
		raw = jobs.Get([]byte(link.JobID))
		if raw == nil {
			return nil
		}
		job = new(commandJobState)
		if err := gob.NewDecoder(bytes.NewReader(raw)).Decode(job); err != nil {
			return err
		}
		if job.View.ID != link.JobID || !job.HistoryDeleted && !publicJobDiagnostics(job.View) {
			return errors.New("invalid stored job identity or state")
		}
		job.View.AuthorizationURL, job.View.Email = "", ""
		job.View.PlanType, _ = auth.ParsePlan(job.View.PlanType)
		return nil
	})
	if err != nil {
		return nil, nil, errors.New("storage_unavailable")
	}
	return link, job, nil
}

// A missing link on a pre-upgrade or interrupted reservation cannot establish
// which job belongs to it. Never substitute the account's latest job.
func (s *Service) lookupJobCommand(key string) (jobCommandReceipt, error) {
	if !validCommandKey(key) {
		return jobCommandReceipt{}, errors.New("invalid_request")
	}
	record, job, err := s.journal.readJobCommand(s.journal.fingerprint(key))
	if err != nil {
		return jobCommandReceipt{}, err
	}
	if record == nil {
		return jobCommandReceipt{}, errors.New("command_not_accepted")
	}
	result := jobCommandReceipt{ID: key, Path: record.JobPath}
	switch {
	case record.Retired:
		result.Status, result.Code = "expired", "command_result_expired"
	case record.JobID != "":
		// Legacy completed reservations already contain a job ID. Non-job
		// commands can also have an id field, so corroborate the actual job.
		if result.Path == "" && job != nil && !job.HistoryDeleted {
			switch {
			case job.View.Action != "":
				result.Path = "/accounts/security"
			case job.View.AccountID != "":
				result.Path = "/accounts/start"
			default:
				result.Path = "/start"
			}
		}
		if result.Path == "" {
			result.Status, result.Code, result.OutcomeUnknown = "reconciliation_required", "idempotency_unknown", true
			break
		}
		result.JobID = record.JobID
		if job == nil || job.HistoryDeleted {
			result.Status, result.Code = "expired", "command_result_expired"
			break
		}
		result.Job, result.Status = &job.View, "accepted"
	case record.JobPath == "" || record.Pending:
		result.Status, result.Code, result.OutcomeUnknown = "reconciliation_required", "idempotency_unknown", true
	case record.Code != "":
		if !publicStoredCode(record.Code) {
			return jobCommandReceipt{}, errors.New("storage_unavailable")
		}
		result.Status, result.Code = "failed", record.Code
	default:
		result.Status, result.Code, result.OutcomeUnknown = "reconciliation_required", "idempotency_unknown", true
	}
	return result, nil
}
