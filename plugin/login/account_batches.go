package login

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/cliproxyapi-plugins/model-quality/login/conversion"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	bolt "go.etcd.io/bbolt"
)

const batchBucket = "account-batches-v1"
const activeBatchBucket = "active"
const batchQueueDepth = 3

var errBatchMissing = errors.New("not_found")

// Only display-safe progress and the current write intent cross a restart.
// The source document, converted credentials and SDK context stay in memory.
type batchReceipt struct {
	CancelRequested bool                `json:"cancel_requested,omitempty"`
	CommitID        string              `json:"-"`
	ID              string              `json:"id,omitempty"`
	Kind            string              `json:"kind"`
	Status          string              `json:"status"`
	Total           int                 `json:"total"`
	Completed       int                 `json:"completed"`
	Code            string              `json:"code,omitempty"`
	Items           []conversionOutcome `json:"items,omitempty"`
	InspectID       string              `json:"account_id,omitempty"`
	Intent          *conversionOutcome  `json:"-"`
}

func (v batchReceipt) active() bool {
	return v.Status == "accepted" || v.Status == "running" || v.Status == "cancelling"
}

type accountBatchWork struct {
	id       string // HMAC of the client key
	view     batchReceipt
	accounts []string
	uploads  []conversion.Item
	done     chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
}

type accountBatches struct {
	mu         sync.Mutex
	inspection chan *accountBatchWork
	upload     chan *accountBatchWork
	pending    map[string]*accountBatchWork
	closed     bool
}

func (b *accountBatches) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	close(b.inspection)
	close(b.upload)
}

func (s *Service) startAccountBatches() error {
	if err := s.recoverAccountBatches(); err != nil {
		return err
	}
	b := &s.batches
	b.pending = make(map[string]*accountBatchWork)
	b.inspection = make(chan *accountBatchWork, batchQueueDepth)
	b.upload = make(chan *accountBatchWork, batchQueueDepth)
	for _, queue := range []chan *accountBatchWork{b.inspection, b.upload} {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			for work := range queue {
				if err := s.runAccountBatch(work); err != nil {
					// A local progress commit failed. Stop before another external call.
					_ = s.accountStorageFailure()
				}
				work.cancel()
				b.mu.Lock()
				close(work.done)
				delete(b.pending, work.id)
				b.mu.Unlock()
			}
		}()
	}
	return nil
}

func (s *Service) lookupAccountBatch(key string) (batchReceipt, error) {
	if !validCommandKey(key) {
		return batchReceipt{}, errors.New("invalid_request")
	}
	id := s.journal.fingerprint(key)
	view, err := s.journal.batchByID(id)
	if errors.Is(err, errBatchMissing) {
		record, readErr := s.journal.commandRecord(id)
		if readErr != nil {
			return batchReceipt{}, readErr
		}
		if record != nil {
			return batchReceipt{}, errors.New("idempotency_unknown")
		}
		return batchReceipt{}, errors.New("command_not_accepted")
	}
	if err == nil {
		// After a disk failure an in-flight outcome may be newer than the durable row.
		if s.ctx.Err() != nil && view.active() {
			return batchReceipt{}, errors.New("idempotency_unknown")
		}
		view.ID = key
	}
	return view, err
}

type batchAdmission struct {
	view   batchReceipt
	done   <-chan struct{}
	legacy bool
}

func (s *Service) admitAccountBatch(path, key string, raw []byte) (batchAdmission, error) {
	if !validCommandKey(key) {
		return batchAdmission{}, errors.New("invalid_request")
	}
	canonical, err := canonicalRequest(raw)
	if err != nil {
		return batchAdmission{}, err
	}
	j := s.journal
	id, fingerprint := j.fingerprint(key), j.fingerprint("POST"+path, canonical)
	release := j.lockCommand(id)
	defer release()
	prior, err := j.commandRecord(id)
	if err != nil {
		return batchAdmission{}, err
	}
	if prior != nil {
		if err := prior.check(fingerprint); err != nil {
			return batchAdmission{}, err
		}
		switch prior.BatchVersion {
		case 0:
			return batchAdmission{legacy: true}, nil
		case 1:
			s.batches.mu.Lock()
			defer s.batches.mu.Unlock()
			view, err := s.lookupAccountBatch(key)
			receipt := batchAdmission{view: view}
			if active := s.batches.pending[id]; active != nil {
				receipt.done = active.done
			}
			return receipt, err
		default:
			return batchAdmission{}, errors.New("storage_unavailable")
		}
	}
	if err := j.admit(); err != nil {
		return batchAdmission{}, err
	}
	work, err := s.prepareAccountBatch(path, id, raw)
	if err != nil {
		return batchAdmission{}, err
	}
	return s.enqueueAccountBatch(key, fingerprint, work)
}

func (s *Service) prepareAccountBatch(path, id string, raw []byte) (*accountBatchWork, error) {
	work := &accountBatchWork{id: id, done: make(chan struct{})}
	switch path {
	case "/accounts/inspect":
		input, err := Object(raw, "id")
		if err != nil {
			return nil, err
		}
		work.view.InspectID, err = stringField(input, "id")
		if err != nil || len(work.view.InspectID) > 512 {
			return nil, errors.New("invalid_request")
		}
		work.accounts, err = s.inspectionSelection(work.view.InspectID)
		if err != nil {
			return nil, err
		}
		work.view.Kind, work.view.Total = "inspection", len(work.accounts)
	case "/conversion/upload":
		items, err := selectedConversions(raw, s.opts.Now())
		if err != nil {
			return nil, err
		}
		work.uploads = items
		work.view.Kind, work.view.Total = "conversion", len(items)
	default:
		return nil, errors.New("not_found")
	}
	work.view.Status = "accepted"
	return work, nil
}

func (s *Service) enqueueAccountBatch(key, fingerprint string, work *accountBatchWork) (batchAdmission, error) {
	b := &s.batches
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || s.ctx.Err() != nil {
		return batchAdmission{}, errors.New("plugin_unavailable")
	}
	queue := b.inspection
	if work.view.Kind == "conversion" {
		queue = b.upload
	}
	if len(queue) == cap(queue) {
		return batchAdmission{}, errors.New("queue_full")
	}
	if err := s.journal.acceptAccountBatch(work.id, fingerprint, work.view); err != nil {
		return batchAdmission{}, err
	}
	view := work.view
	view.ID = key
	work.ctx, work.cancel = context.WithCancel(s.ctx)
	b.pending[work.id] = work
	queue <- work
	return batchAdmission{view: view, done: work.done}, nil
}

func (s *Service) inspectionSelection(id string) ([]string, error) {
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	if err := s.accountReady(); err != nil {
		return nil, err
	}
	if id != "" {
		if _, ok := s.accounts[id]; !ok {
			return nil, errors.New("not_found")
		}
		return []string{id}, nil
	}
	ids := make([]string, 0, len(s.accounts))
	for id := range s.accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Service) runAccountBatch(work *accountBatchWork) error {
	v := &work.view
	if work.ctx.Err() != nil {
		v.finish(s.batchStopReason())
		return s.journal.updateBatch(work.id, *v)
	}
	v.Status = "running"
	if err := s.journal.updateBatch(work.id, *v); err != nil {
		return err
	}
	var owner Owner
	var opErr error
	if v.Kind == "conversion" {
		owner, opErr = s.opts.Owner()
		if opErr != nil || owner.Valid == nil || owner.Prepare == nil || !owner.Valid() {
			opErr = errors.New("plugin_unavailable")
		}
	} else {
		s.accountMu.Lock()
		opErr = s.syncAccountJobsLocked()
		s.accountMu.Unlock()
	}
	for i := 0; opErr == nil && i < v.Total; i++ {
		if work.ctx.Err() != nil {
			opErr = s.batchStopReason()
			break
		}
		if v.Kind == "conversion" {
			item := work.uploads[i]
			v.Intent = &conversionOutcome{Index: item.Index, Filename: item.Filename}
			v.CommitID = "conversion:" + conversion.Identity(item)
			if err := s.journal.updateBatch(work.id, *v); err != nil {
				return err
			}
			row := s.uploadConvertedItem(work.ctx, owner, item, work.id)
			work.uploads[i] = conversion.Item{}
			v.Items = append(v.Items, row)
			v.Intent, v.CommitID = nil, ""
		} else {
			opErr = s.inspectSelectedAccount(work.accounts[i])
			if opErr != nil {
				break
			}
		}
		v.Completed++
		// Each upload is a side effect. Read-only inspections checkpoint in groups.
		if v.Completed < v.Total && (v.Kind == "conversion" || v.Completed%16 == 0) {
			if err := s.journal.updateBatch(work.id, *v); err != nil {
				return err
			}
		}
	}
	if work.ctx.Err() != nil && opErr == nil && (v.Completed < v.Total || len(v.Items) > 0 && v.Items[len(v.Items)-1].Code == "cancelled") {
		opErr = s.batchStopReason()
	}
	v.finish(opErr)
	return s.journal.updateBatch(work.id, *v)
}

func (s *Service) batchStopReason() error {
	if s.ctx.Err() != nil {
		return errors.New("interrupted")
	}
	return errors.New("cancelled")
}

func (v *batchReceipt) finish(err error) {
	v.Status, v.Code = "completed", SafeCode(err)
	switch v.Code {
	case "":
	case "interrupted", "cancelled":
		v.Status = v.Code
	default:
		v.Status = "failed"
	}
	for _, row := range v.Items {
		if row.Status == "uncertain" {
			v.Status, v.Code = "reconciliation_required", row.Code
			return
		}
		if row.Status == "failed" && v.Status == "completed" {
			v.Status, v.Code = "failed", row.Code
		}
	}
}

func (s *Service) cancelAccountBatch(key string) (any, error) {
	if !validCommandKey(key) {
		return nil, errors.New("invalid_request")
	}
	id := s.journal.fingerprint(key)
	b := &s.batches
	b.mu.Lock()
	defer b.mu.Unlock()
	var receipt batchReceipt
	err := s.journal.db.Update(func(tx *bolt.Tx) error {
		var err error
		receipt, err = getBatch(tx, id)
		if err != nil {
			return err
		}
		if !receipt.active() {
			return nil
		}
		if b.pending[id] == nil {
			return errors.New("idempotency_unknown")
		}
		receipt.Status, receipt.CancelRequested = "cancelling", true
		return saveBatch(tx, id, receipt)
	})
	if err != nil {
		return nil, err
	}
	if w := b.pending[id]; w != nil && receipt.CancelRequested {
		w.cancel()
	}
	return map[string]any{"status": receipt.Status, "cancel_requested": receipt.CancelRequested}, nil
}

func (s *Service) handleAccountBatch(ctx context.Context, req pluginapi.ManagementRequest, path string) pluginapi.ManagementResponse {
	key := req.Headers.Get("Idempotency-Key")
	receipt, err := s.admitAccountBatch(path, key, req.Body)
	if err != nil {
		return errorReply(err)
	}
	if receipt.legacy {
		// Reconstruct a pre-upgrade synchronous receipt through the existing projection.
		raw, err := s.Idempotent(key, req.Method+path, req.Body, func() (any, error) { return nil, errors.New("idempotency_unknown") })
		if err != nil {
			return errorReply(err)
		}
		r := reply(200, nil)
		r.Body = raw
		return r
	}
	if strings.EqualFold(req.Headers.Get("Prefer"), "respond-async") {
		return reply(http.StatusAccepted, receipt.view)
	}
	if receipt.done != nil {
		select {
		case <-receipt.done:
		case <-ctx.Done():
			return reply(http.StatusAccepted, receipt.view)
		}
	}
	view, err := s.lookupAccountBatch(key)
	if err != nil {
		return errorReply(err)
	}
	if view.Kind == "conversion" && (view.Completed == view.Total || len(view.Items) > 0) {
		return reply(200, view)
	}
	if view.Status != "completed" {
		if view.Code == "" {
			view.Code = "idempotency_unknown"
		}
		return errorReply(errors.New(view.Code))
	}
	items, err := s.accountList(view.InspectID)
	if err != nil {
		return errorReply(err)
	}
	return reply(200, map[string]any{"items": items})
}
