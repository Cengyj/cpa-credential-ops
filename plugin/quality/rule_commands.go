package quality

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/MoeForge/moego/json"
	bolt "go.etcd.io/bbolt"
)

const ruleCommandsBucket = "rule_commands_v1"
const pendingRuleCommandsBucket = "rule_commands_pending_v1"
const ruleCommandBucketsKey = "rule_command_buckets_v1"

// Completed run commands describe admission. Individual model results remain
// on the referenced runs. Receipts do not retain the rule or its prompts.
type ruleCommandReceipt struct {
	Key      string        `json:"idempotency_key"`
	Kind     string        `json:"kind"`
	State    string        `json:"state"`
	RuleID   string        `json:"rule_id"`
	Revision uint64        `json:"revision"`
	Result   *ruleDispatch `json:"result,omitempty"`
	Error    *queryFailure `json:"error,omitempty"`
}

type ruleCommandRecord struct {
	ruleCommandReceipt
	RequestHash string `json:"request_hash"`
}

func validateRuleCommandKey(key string) error {
	if key == "" || len(key) > 128 || strings.IndexFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("invalid_request: invalid rule command idempotency_key")
	}
	return nil
}

func (en *Engine) findRuleCommand(key, hash string) (*ruleCommandRecord, error) {
	if err := validateRuleCommandKey(key); err != nil {
		return nil, err
	}
	var record *ruleCommandRecord
	err := en.store.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(ruleCommandsBucket))
		if b == nil || b.Get([]byte(key)) == nil {
			return nil
		}
		record = new(ruleCommandRecord)
		return json.Unmarshal(b.Get([]byte(key)), record)
	})
	if err != nil {
		return nil, fmt.Errorf("unavailable: read rule command: %w", err)
	}
	if record != nil && hash != "" && record.RequestHash != hash {
		return nil, fmt.Errorf("conflict: rule command key reused with different parameters")
	}
	return record, nil
}

func (en *Engine) ruleCommandStatus(key string) (*ruleCommandReceipt, error) {
	r, err := en.findRuleCommand(key, "")
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("command_not_accepted: rule command has not been accepted")
	}
	en.mu.Lock()
	defer en.mu.Unlock()
	return en.ruleCommandViewLocked(r), nil
}

func (en *Engine) ruleCommandViewLocked(r *ruleCommandRecord) *ruleCommandReceipt {
	view := r.ruleCommandReceipt
	if (view.State == "running" || view.State == "accepted") && en.fault != nil {
		view.State = "awaiting_verification"
		view.Error = &queryFailure{Code: "command_store_uncertain", Source: "plugin", OutcomeUnknown: true, Message: "质量库写入未确认，需恢复存储后核对该命令收据。"}
	}
	return &view
}

// The marker records the last bucket created in the receipts-then-pending
// sequence. Old databases may legitimately have neither bucket or receipts only.
func prepareRuleCommandBuckets(tx *bolt.Tx) error {
	meta := tx.Bucket([]byte("meta"))
	marker := meta.Get([]byte(ruleCommandBucketsKey))
	receipts := tx.Bucket([]byte(ruleCommandsBucket))
	pending := tx.Bucket([]byte(pendingRuleCommandsBucket))
	if receipts == nil {
		if marker != nil || pending != nil {
			return errors.New("incomplete quality store: rule command receipts are missing")
		}
		return nil
	}
	latest := ruleCommandsBucket
	if pending != nil {
		latest = pendingRuleCommandsBucket
	}
	if marker != nil {
		if string(marker) == latest {
			return nil
		}
		switch string(marker) {
		case pendingRuleCommandsBucket:
			return errors.New("incomplete quality store: rule command pending index is missing")
		case ruleCommandsBucket:
			// An older binary may have created the pending bucket since the marker.
		default:
			return errors.New("unsupported quality store rule command bucket marker")
		}
	}
	// The old writer also committed run receipts and pending entries together.
	// Verify the existing index once before adopting it.
	if err := verifyLegacyRuleCommandIndex(receipts, pending); err != nil {
		return err
	}
	return meta.Put([]byte(ruleCommandBucketsKey), []byte(latest))
}

func verifyLegacyRuleCommandIndex(receipts, pending *bolt.Bucket) error {
	err := receipts.ForEach(func(key, raw []byte) error {
		var header struct {
			Key   string `json:"idempotency_key"`
			Kind  string `json:"kind"`
			State string `json:"state"`
		}
		if json.Unmarshal(raw, &header) != nil || header.Key != string(key) || header.Kind != "run" && header.Kind != "create" {
			return errors.New("incomplete quality store: invalid rule command receipt")
		}
		if header.Kind == "run" && pending == nil {
			return errors.New("incomplete quality store: rule run receipt without pending index")
		}
		active := header.State == "accepted" || header.State == "running"
		indexed := pending != nil && pending.Get(key) != nil
		if active != indexed || active && header.Kind != "run" {
			return errors.New("incomplete quality store: rule command state disagrees with pending index")
		}
		return nil
	})
	if err != nil || pending == nil {
		return err
	}
	return pending.ForEach(func(key, value []byte) error {
		if len(value) != 1 || value[0] != 1 || receipts.Get(key) == nil {
			return errors.New("incomplete quality store: invalid pending rule command")
		}
		return nil
	})
}

func ensureRuleCommandBucket(tx *bolt.Tx, name string) (*bolt.Bucket, error) {
	if existing := tx.Bucket([]byte(name)); existing != nil {
		return existing, nil
	}
	if err := prepareRuleCommandBuckets(tx); err != nil {
		return nil, err
	}
	b, err := tx.CreateBucket([]byte(name))
	if err != nil {
		return nil, err
	}
	return b, tx.Bucket([]byte("meta")).Put([]byte(ruleCommandBucketsKey), []byte(name))
}

func putRuleCommand(tx *bolt.Tx, r *ruleCommandRecord) error {
	if _, err := ensureRuleCommandBucket(tx, ruleCommandsBucket); err != nil {
		return err
	}
	if err := put(tx, ruleCommandsBucket, r.Key, r); err != nil {
		return err
	}
	if r.State == "accepted" || r.State == "running" {
		b, err := ensureRuleCommandBucket(tx, pendingRuleCommandsBucket)
		if err != nil {
			return err
		}
		return b.Put([]byte(r.Key), []byte{1})
	}
	if b := tx.Bucket([]byte(pendingRuleCommandsBucket)); b != nil {
		return b.Delete([]byte(r.Key))
	}
	return nil
}

// Only in-flight keys need startup work. A committed dispatch writes its final
// receipt with its child runs; all other interrupted admissions are terminal.
func (en *Engine) interruptRuleCommands() error {
	return en.store.Write(func(tx *bolt.Tx) error {
		pending := tx.Bucket([]byte(pendingRuleCommandsBucket))
		if pending == nil {
			return nil
		}
		records := tx.Bucket([]byte(ruleCommandsBucket))
		var updates []ruleCommandRecord
		err := pending.ForEach(func(key, _ []byte) error {
			if records == nil || records.Get(key) == nil {
				return fmt.Errorf("pending rule command without receipt")
			}
			var r ruleCommandRecord
			if err := json.Unmarshal(records.Get(key), &r); err != nil {
				return err
			}
			if r.Key != string(key) || r.Kind != "run" || r.State != "accepted" && r.State != "running" {
				return fmt.Errorf("inconsistent pending rule command")
			}
			r.State = "interrupted"
			r.Error = &queryFailure{Code: "interrupted", Source: "plugin", Message: "服务在受理批次前已停止。该命令已中断。"}
			updates = append(updates, r)
			return nil
		})
		if err != nil {
			return err
		}
		for i := range updates {
			if err := putRuleCommand(tx, &updates[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

func ruleCreation(in Rule, activation string) *ruleCommandRecord {
	if in.IdempotencyKey == "" {
		return nil
	}
	return &ruleCommandRecord{
		ruleCommandReceipt: ruleCommandReceipt{Key: in.IdempotencyKey, Kind: "create", State: "completed"},
		RequestHash: fingerprint(struct {
			Kind, Activation, ExpectedType string
			Rule                           Rule
		}{"create", activation, in.expectedTargetObjectType, in}),
	}
}

func (en *Engine) previousCreationLocked(cmd *ruleCommandRecord) (*Rule, error) {
	if cmd == nil {
		return nil, nil
	}
	prior, err := en.findRuleCommand(cmd.Key, cmd.RequestHash)
	if err != nil || prior == nil {
		return nil, err
	}
	r := en.rules[prior.RuleID]
	if r == nil {
		return nil, fmt.Errorf("gone: this command created a rule which has since been deleted")
	}
	view := en.ruleForDisplayLocked(r)
	return &view, nil
}

func (en *Engine) acceptRuleRun(ctx context.Context, key, id string, revision uint64) (*ruleCommandReceipt, error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	hash := fingerprint(struct {
		Kind, ID string
		Revision uint64
	}{"run", id, revision})
	prior, err := en.findRuleCommand(key, hash)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		return en.ruleCommandViewLocked(prior), nil
	}
	if err := en.mutableLocked(); err != nil {
		return nil, err
	}
	rule := en.rules[id]
	if rule == nil {
		return nil, fmt.Errorf("not_found: rule")
	}
	if revision == 0 || rule.Revision != revision {
		return nil, fmt.Errorf("conflict: stale rule revision")
	}
	if !rule.Enabled || ruleStatus(rule) != "detecting" || en.unsupportedRule(rule) {
		return nil, fmt.Errorf("blocked: rule is stopped or unsupported")
	}
	if en.scanning[id] || en.commandAdmissions[id] != "" || en.hasCurrentRoundLocked(rule) {
		return nil, fmt.Errorf("conflict: rule already running or scanning")
	}
	if len(en.commandAdmissions) >= max(1, en.cfg.Workers) {
		return nil, fmt.Errorf("queue_full: rule admission workers are busy")
	}
	if err := en.storageAdmission(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := ruleCommandRecord{ruleCommandReceipt: ruleCommandReceipt{Key: key, Kind: "run", RuleID: id, Revision: revision, State: "accepted"}, RequestHash: hash}
	if err := en.store.Write(func(tx *bolt.Tx) error { return putRuleCommand(tx, &cmd) }); err != nil {
		return nil, err
	}
	if en.commandAdmissions == nil {
		en.commandAdmissions = make(map[string]string)
	}
	en.commandAdmissions[id] = key
	en.wg.Add(1)
	go en.executeRuleCommand(cmd)
	return &cmd.ruleCommandReceipt, nil
}

func (en *Engine) executeRuleCommand(cmd ruleCommandRecord) {
	defer en.wg.Done()
	defer func() {
		en.mu.Lock()
		delete(en.commandAdmissions, cmd.RuleID)
		en.mu.Unlock()
	}()
	cmd.State = "running"
	if err := en.store.Write(func(tx *bolt.Tx) error { return putRuleCommand(tx, &cmd) }); err != nil {
		en.mu.Lock()
		en.persistFailure(err)
		en.mu.Unlock()
		return
	}
	_, err := en.dispatchRulePlan(en.ctx, cmd.RuleID, cmd.Revision, false, en.host.Targets, nil, &cmd)
	if err == nil {
		return
	}
	// A failed durability barrier can leave the write outcome uncertain.
	// Preserve that exact transaction for read-only recovery on reopen.
	en.mu.Lock()
	faulted := en.fault != nil
	en.mu.Unlock()
	if faulted {
		return
	}
	cmd.State = "failed"
	failure, _ := describeFailure(err, http.MethodPost)
	cmd.Error = &failure
	if errors.Is(err, context.Canceled) || en.ctx.Err() != nil {
		cmd.State = "interrupted"
		cmd.Error = &queryFailure{Code: "interrupted", Source: "plugin", Message: "服务在受理批次前已停止。该命令已中断。"}
	}
	if err := en.store.Write(func(tx *bolt.Tx) error { return putRuleCommand(tx, &cmd) }); err != nil {
		en.mu.Lock()
		en.persistFailure(err)
		en.mu.Unlock()
	}
}

func completedDispatch(cmd *ruleCommandRecord, result ruleDispatch) *ruleCommandRecord {
	if cmd == nil {
		return nil
	}
	finished := *cmd
	finished.State, finished.Result = "completed", &result
	return &finished
}
