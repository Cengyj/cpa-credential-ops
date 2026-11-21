package quality

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/json"
	log "github.com/sirupsen/logrus"
	bolt "go.etcd.io/bbolt"
)

// The SDK can omit files during reload. Only the file-specific CPA 404
// confirms absence. Never read a successful credential download.
func (h *NativeHost) missingCredentialFile(ctx context.Context, name string) bool {
	resp, err := h.Management.response(ctx, http.MethodGet, "/v8/management/credentials/download?name="+url.QueryEscape(name), nil)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		return false
	}
	var reply struct {
		Error string `json:"error"`
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
	return err == nil && len(raw) <= 1024 && json.Unmarshal(raw, &reply) == nil && reply.Error == "file not found"
}

type credentialDirectory struct {
	ids, indices, names map[string]bool
}

func (h *NativeHost) credentialDirectory(ctx context.Context) (*credentialDirectory, error) {
	var listed struct {
		Files []struct {
			ID    string `json:"id"`
			Index string `json:"auth_index"`
			Name  string `json:"name"`
		} `json:"files"`
	}
	if err := h.call(ctx, "host.auth.list", struct{}{}, &listed); err != nil {
		return nil, err
	}
	if listed.Files == nil {
		return nil, fmt.Errorf("unavailable: CPA file inventory incomplete")
	}
	dir := &credentialDirectory{map[string]bool{}, map[string]bool{}, map[string]bool{}}
	for _, file := range listed.Files {
		dir.ids[file.ID], dir.indices[file.Index], dir.names[strings.ToLower(file.Name)] = true, true, true
	}
	return dir, nil
}

func (dir *credentialDirectory) absent(s CredentialSnapshot) bool {
	return s.AuthID != "" && s.AuthIndex != "" && s.Name != "" && !dir.ids[s.AuthID] && !dir.indices[s.AuthIndex] && !dir.names[strings.ToLower(s.Name)]
}

func (h *NativeHost) removedCredentials(ctx context.Context, candidates []*CredentialDisposition, workers int) ([]*CredentialDisposition, error) {
	dir, err := h.credentialDirectory(ctx)
	if err != nil {
		return nil, err
	}
	var wg sync.WaitGroup
	slots := make(chan struct{}, workers)
	missing := make([]bool, len(candidates))
	for i, d := range candidates {
		s := d.Snapshot
		if !dir.absent(s) {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			missing[i] = h.missingCredentialFile(ctx, s.Name)
		})
	}
	wg.Wait()
	var removed []*CredentialDisposition
	for i, ok := range missing {
		if ok {
			removed = append(removed, candidates[i])
		}
	}
	return removed, nil
}

// Queued cancellations and workers releasing their reservations each finish one child.
func (en *Engine) finishRuleChildLocked(f Frozen) {
	if f.ScopeBatchID != "" {
		if remaining := en.roundRemaining[f.ScopeBatchID]; remaining > 1 {
			en.roundRemaining[f.ScopeBatchID] = remaining - 1
			return
		}
		delete(en.roundRemaining, f.ScopeBatchID)
	}
	en.noteRoundEndLocked(f.RuleID)
}

func (en *Engine) noteRoundEndLocked(ruleID string) {
	if ruleID == "" || len(en.managed) == 0 {
		return
	}
	if en.finishedRules == nil {
		en.finishedRules = make(map[string]bool)
	}
	en.finishedRules[ruleID] = true
	select {
	case en.roundEnded <- struct{}{}:
	default:
	}
}

func (en *Engine) watchFinishedRounds() {
	defer en.wg.Done()
	for {
		select {
		case <-en.ctx.Done():
			return
		case <-en.roundEnded:
			if err := en.checkFinishedRounds(en.ctx); err != nil {
				log.Debug("model-quality: could not verify removed credentials at round end")
			}
		}
	}
}

// Coalesce finished rounds and query CPA once for all ready rules.
func (en *Engine) checkFinishedRounds(ctx context.Context) error {
	en.mu.Lock()
	if len(en.finishedRules) == 0 {
		en.mu.Unlock()
		return nil
	}
	ready := en.finishedRules
	en.finishedRules = nil
	h, ok := en.host.(interface {
		removedCredentials(context.Context, []*CredentialDisposition, int) ([]*CredentialDisposition, error)
	})
	if !ok || !en.Capabilities().ManagementSession {
		en.mu.Unlock()
		return nil
	}
	var candidates []*CredentialDisposition
	for _, d := range en.managed {
		if !ready[d.RuleID] || d.Snapshot.ProviderMechanism != "" || errorPolicyDisabled(d).Code != "" {
			continue
		}
		switch d.State {
		case "disabled", "restored", "externally_enabled", "acknowledged":
			candidates = append(candidates, d)
		}
	}
	workers := en.cfg.Workers
	err := en.mutableLocked()
	en.mu.Unlock()
	if err != nil || len(candidates) == 0 {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	confirmed, err := h.removedCredentials(probeCtx, candidates, workers)
	if err != nil || len(confirmed) == 0 {
		return err
	}

	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	removed := make(map[string]*CredentialDisposition)
	for _, d := range confirmed {
		_, busy := en.busyAuth[d.AuthID]
		if !busy && en.managed[d.AuthID] == d && en.localPause(RunConfig{AuthID: d.AuthID}) == "" {
			removed[d.AuthID] = d
		}
	}
	for _, r := range en.runs {
		if r.Status == "queued" || r.Status == "running" || en.active[r.ID] != nil {
			for _, id := range dependencies(r.Config.RunConfig) {
				delete(removed, id)
			}
		}
	}
	if len(removed) == 0 {
		return nil
	}
	err = en.store.Write(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte("meta"))
		for id := range removed {
			if err := meta.Delete([]byte(dispositionPrefix + id)); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		for id := range removed {
			delete(en.managed, id)
		}
		en.directory.revision.Add(1)
	}
	return err
}
