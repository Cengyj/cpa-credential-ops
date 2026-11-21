package login

import (
	"bytes"
	"encoding/gob"
	"errors"
	bolt "go.etcd.io/bbolt"
)

// The idempotency reservation, progress receipt and active index form one commit.
func (j *journal) acceptAccountBatch(id, fingerprint string, view batchReceipt) error {
	err := j.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(batchBucket))
		if err != nil {
			return err
		}
		if _, err = bucket.CreateBucketIfNotExists([]byte(activeBatchBucket)); err != nil {
			return err
		}
		if err = putGob(tx, "idempotency", id, idempotency{BatchVersion: 1, Fingerprint: fingerprint, Created: j.now()}); err != nil {
			return err
		}
		return saveBatch(tx, id, view)
	})
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}

func getBatch(tx *bolt.Tx, id string) (batchReceipt, error) {
	var view batchReceipt
	b := tx.Bucket([]byte(batchBucket))
	if b == nil || b.Get([]byte(id)) == nil {
		return view, errBatchMissing
	}
	if err := gob.NewDecoder(bytes.NewReader(b.Get([]byte(id)))).Decode(&view); err != nil {
		return batchReceipt{}, errors.New("storage_unavailable")
	}
	return view, nil
}

func (j *journal) batchByID(id string) (batchReceipt, error) {
	var view batchReceipt
	err := j.db.View(func(tx *bolt.Tx) error {
		var err error
		view, err = getBatch(tx, id)
		return err
	})
	if err != nil && !errors.Is(err, errBatchMissing) {
		return batchReceipt{}, errors.New("storage_unavailable")
	}
	return view, err
}

const unconfirmedBatchBucket = "unconfirmed-conversions"

// The account-side barrier survives loss of the SDK-side receipt. Only the
// original batch can resolve it. Observing it from another command cannot
// transfer ownership or authorize any new external write.
func (j *journal) unconfirmedConversion(commitID string) (bool, error) {
	var blocked bool
	err := j.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(batchBucket))
		if b != nil {
			if held := b.Bucket([]byte(unconfirmedBatchBucket)); held != nil {
				blocked = held.Get([]byte(commitID)) != nil
			}
		}
		return nil
	})
	if err != nil {
		return false, errors.New("storage_unavailable")
	}
	return blocked, nil
}

func saveBatch(tx *bolt.Tx, id string, view batchReceipt) error {
	b := tx.Bucket([]byte(batchBucket))
	if err := putGob(tx, batchBucket, id, view); err != nil {
		return err
	}
	for _, row := range view.Items {
		if row.Status != "uncertain" || row.CommitRef == "" {
			continue
		}
		held, err := b.CreateBucketIfNotExists([]byte(unconfirmedBatchBucket))
		if err != nil {
			return err
		}
		if held.Get([]byte(row.CommitRef)) == nil {
			if err := held.Put([]byte(row.CommitRef), []byte(id)); err != nil {
				return err
			}
		}
	}
	active := b.Bucket([]byte(activeBatchBucket))
	if view.active() {
		return active.Put([]byte(id), []byte{1})
	}
	return active.Delete([]byte(id))
}

func (j *journal) updateBatch(id string, view batchReceipt) error {
	if err := j.capacity(true); err != nil {
		return err
	}
	return j.db.Update(func(tx *bolt.Tx) error {
		previous, err := getBatch(tx, id)
		if err != nil {
			return err
		}
		if previous.CancelRequested {
			view.CancelRequested = true
			if view.active() {
				view.Status = "cancelling"
			}
		}
		return saveBatch(tx, id, view)
	})
}

// Only a complete receipt for this batch and locator can settle a row. A
// missing receipt is unknown. A corrupt/unreadable store aborts the local
// transaction so a repaired backup can be checked at the next startup.
func (s *Service) reconcileConversionRow(id string, row *conversionOutcome) (bool, error) {
	result, err := s.opts.Manager.LoginCommitStatus(row.CommitRef)
	if err != nil {
		if SafeCode(err) == "not_found" {
			return false, nil
		}
		return false, errors.New("storage_unavailable")
	}
	if result.CommandRef != id || result.AuthID != row.Filename {
		return false, nil
	}
	switch result.State {
	case "committed":
		if result.Name != row.Filename || result.AuthIndex == "" || result.IdentityVersion == "" || result.Principal == "" {
			return false, errors.New("storage_unavailable")
		}
		row.Status, row.Code, row.AuthID, row.Enabled = "uploaded", "", result.AuthID, &result.CredentialEnabled
	case "aborted":
		row.Status, row.Code = "failed", "interrupted"
	default:
		return false, nil
	}
	return true, nil
}

func (s *Service) reconcileHeldConversions(tx *bolt.Tx, b *bolt.Bucket) error {
	held := b.Bucket([]byte(unconfirmedBatchBucket))
	if held == nil {
		return nil
	}
	var refs []string
	if err := held.ForEach(func(k, _ []byte) error { refs = append(refs, string(k)); return nil }); err != nil {
		return err
	}
	for _, ref := range refs {
		id := string(held.Get([]byte(ref)))
		view, err := getBatch(tx, id)
		if err != nil {
			return err
		}
		found := false
		for i := range view.Items {
			row := &view.Items[i]
			if row.CommitRef != ref || row.Status != "uncertain" {
				continue
			}
			found = true
			ok, err := s.reconcileConversionRow(id, row)
			if err != nil {
				return err
			}
			if ok {
				if err := held.Delete([]byte(ref)); err != nil {
					return err
				}
				if !view.active() {
					view.finishAfterRestart()
				}
				if err := saveBatch(tx, id, view); err != nil {
					return err
				}
			}
			break
		}
		if !found {
			return errors.New("storage_unavailable")
		}
	}
	return nil
}

func (view *batchReceipt) finishAfterRestart() {
	var interruption error
	if view.Total != view.Completed || view.Completed == 0 {
		interruption = errors.New("interrupted")
		if view.CancelRequested {
			interruption = errors.New("cancelled")
		}
	}
	view.finish(interruption)
}

func (s *Service) recoverAccountBatches() error {
	j := s.journal
	// The active index bounds startup work even when many completed receipts exist.
	err := j.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(batchBucket))
		if b == nil {
			return nil
		}
		active := b.Bucket([]byte(activeBatchBucket))
		if active == nil {
			return errors.New("storage_unavailable")
		}
		if err := s.reconcileHeldConversions(tx, b); err != nil {
			return err
		}
		var ids []string
		if err := active.ForEach(func(k, _ []byte) error { ids = append(ids, string(k)); return nil }); err != nil {
			return err
		}
		for _, id := range ids {
			view, err := getBatch(tx, id)
			if err != nil || !view.active() {
				return errors.New("storage_unavailable")
			}
			if view.Intent != nil {
				row := *view.Intent
				row.Status, row.Code, row.CommitRef = "uncertain", "commit_unknown", view.CommitID
				if row.CommitRef == "" {
					return errors.New("storage_unavailable")
				}
				if _, err := s.reconcileConversionRow(id, &row); err != nil {
					return err
				}
				view.Items = append(view.Items, row)
				view.Completed++
				view.Intent, view.CommitID = nil, ""
			}
			view.finishAfterRestart()
			if err := saveBatch(tx, id, view); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return errors.New("storage_unavailable")
	}
	return nil
}
