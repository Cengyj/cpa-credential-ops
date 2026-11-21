package quality

import (
	"context"
	"fmt"
	"maps"
)

type directoryAuthorityKey struct{}
type directoryAuthority struct {
	owners          map[string]*CredentialDisposition
	loginGeneration uint64
}

func (en *Engine) inventoryContextLocked(ctx context.Context) context.Context {
	if _, ok := ctx.Value(directoryAuthorityKey{}).(directoryAuthority); ok {
		return ctx
	}
	return context.WithValue(ctx, directoryAuthorityKey{}, directoryAuthority{maps.Clone(en.managed), en.loginGeneration})
}

func (en *Engine) currentDirectoryAuthority(ctx context.Context, id string) bool {
	a, ok := ctx.Value(directoryAuthorityKey{}).(directoryAuthority)
	if !ok {
		return true
	}
	hold := en.loginHolds[id]
	return a.owners[id] == en.managed[id] && (hold == nil || hold.generation <= a.loginGeneration)
}

type snapshotReader func(context.Context, string, string) (CredentialSnapshot, error)

type observationRead struct {
	id, index string
	owner     *CredentialDisposition
	hold      *credentialLoginHold
	snapshot  CredentialSnapshot
	err       error
}

// Collect only snapshots needed to confirm CPA enablement. A full group must
// be checked as one observation. Each captured authority is rechecked under
// lock before publishing, even if another caller observed the same entry.
func (en *Engine) reconcileDirectoryLocked(ctx context.Context, targets []Target, ids []string) map[string]error {
	h, supported := en.host.(managedHost)
	if !supported || len(en.managed) == 0 {
		return en.reconcileCurrentTargetsLocked(ctx, targets, ids)
	}
	needed, groups := map[string]bool{}, map[string]bool{}
	for _, t := range targets {
		d := en.managed[t.AuthID]
		if ids != nil && !contains(ids, t.AuthID) || d == nil || d.State != "disabled" || disabledObject(t) ||
			!ruleSubjectEnabled(t) || !t.identityVerified && !t.catalogVerified || !en.currentDirectoryAuthority(ctx, t.AuthID) {
			continue
		}
		needed[t.AuthID] = true
		if d.Snapshot.ProviderMechanism == "group_disabled" && t.ProviderGroupID != "" {
			groups[t.ProviderGroupID] = true
		}
	}
	reads := make(map[string]*observationRead, len(needed))
	for _, t := range targets {
		if (needed[t.AuthID] || groups[t.ProviderGroupID]) && (!t.IsProvider || en.Capabilities().ManagementSession) {
			reads[t.AuthID] = &observationRead{id: t.AuthID, index: t.AuthIndex, owner: en.managed[t.AuthID], hold: en.loginHolds[t.AuthID]}
		}
	}
	if len(reads) > 0 {
		en.mu.Unlock()
		for _, r := range reads {
			if err := ctx.Err(); err != nil {
				r.err = err
				continue
			}
			r.snapshot, r.err = h.CredentialSnapshot(ctx, r.id, r.index)
		}
		en.mu.Lock()
	}
	return en.reconcileUsingLocked(ctx, targets, ids, func(_ context.Context, id, index string) (CredentialSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return CredentialSnapshot{}, err
		}
		if err := en.mutableLocked(); err != nil {
			return CredentialSnapshot{}, err
		}
		r := reads[id]
		if r == nil || r.index != index || r.owner != en.managed[id] || r.hold != en.loginHolds[id] || !en.currentDirectoryAuthority(ctx, id) {
			return CredentialSnapshot{}, fmt.Errorf("conflict: credential authority changed during CPA verification")
		}
		return r.snapshot, r.err
	})
}
