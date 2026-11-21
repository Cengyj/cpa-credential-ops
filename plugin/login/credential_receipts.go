package login

import (
	"strings"
	"time"

	auth "github.com/cliproxyapi-plugins/model-quality/login/account"
)

const previousBindingPrefix = "previous/"

func (s *Service) finishCredentialWrite(authID, preparationID string, result auth.LoginCommitResult) error {
	if s.opts.FinishWrite == nil {
		return nil
	}
	state := result.State
	if state == "committed" && result.AuthID != authID {
		state = "identity_conflict"
	}
	// A matching file that appeared during a create-only import was never
	// written by this attempt and cannot authorize continuing its old rules.
	if state == auth.ConversionAlreadyPresent || state == auth.ConversionPreviouslyUploaded {
		state = "identity_conflict"
	}
	return s.opts.FinishWrite(authID, preparationID, result.IdentityVersion, state)
}

func (s *Service) monitorCredentialReceipts() {
	defer s.wg.Done()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			s.reconcileCredentialReceipts()
			timer.Reset(15 * time.Second)
		}
	}
}

// Only pending quality holds are visited. A receipt lookup and current SDK
// reads cannot send a login, save a credential, or schedule a model run.
func (s *Service) reconcileCredentialReceipts() {
	for authID, preparationID := range s.opts.PendingWrites() {
		if s.ctx.Err() != nil {
			return
		}
		commitID := strings.TrimPrefix(preparationID, previousBindingPrefix)
		result, err := s.opts.Manager.LoginCommitStatus(commitID)
		if err != nil {
			if err.Error() != "not_found" {
				continue
			}
			// The old task was accepted. A missing receipt across the two
			// stores cannot release its hold or authorize a replacement write.
			result.State = "commit_unknown"
		} else if result.State == "committed" && result.AuthID == authID {
			if err := s.opts.Manager.VerifyLoginReceipt(result); err != nil {
				result.State = "awaiting_verification"
				if err.Error() == "identity_conflict" {
					result.State = "identity_conflict"
				}
			}
		}
		if err := s.finishCredentialWrite(authID, preparationID, result); err != nil {
			return
		}
		if result.State == "committed" && result.AuthID == authID {
			s.mu.Lock()
			if job := s.jobs[commitID]; job != nil && job.record.View.Status == "committed" && job.record.View.ErrorCode == "quality_reconciliation_pending" {
				job.record.View.ErrorCode = ""
				job.record.View.Revision++
				_ = s.persistLocked(job)
			}
			s.mu.Unlock()
		}
	}
}
