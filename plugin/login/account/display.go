package account

import "errors"

// DisplayBinding contains only information used by the account list. Callers
// doing admission or writing credentials must obtain their own current snapshot.
type DisplayBinding struct {
	Principal LoginPrincipal
	Plan      string
	ProxyURL  string
	Err       error
}

func (m *Manager) DisplayBindings(ids map[string]struct{}) map[string]DisplayBinding {
	out := make(map[string]DisplayBinding, len(ids))
	if len(ids) == 0 {
		return out
	}
	entries, err := m.entries()
	if err != nil {
		for id := range ids {
			out[id] = DisplayBinding{Err: err}
		}
		return out
	}
	matches := make(map[string]*Entry, len(ids))
	for i := range entries {
		e := &entries[i]
		for _, id := range []string{e.ID, e.Index, e.Name} {
			if _, wanted := ids[id]; !wanted {
				continue
			}
			if previous, seen := matches[id]; seen && previous != e {
				matches[id] = nil
				continue
			}
			matches[id] = e
		}
	}
	for id := range ids {
		entry, found := matches[id]
		switch {
		case !found:
			out[id] = DisplayBinding{Err: errors.New("not_found")}
		case entry == nil:
			out[id] = DisplayBinding{Err: errors.New("identity_conflict")}
		default:
			current, err := m.read(*entry)
			if err != nil {
				out[id] = DisplayBinding{Err: err}
				continue
			}
			principal, err := LoginPrincipalFromAuth(current)
			out[id] = DisplayBinding{Principal: principal, Plan: LoginPlanType(current), ProxyURL: current.ProxyURL, Err: err}
		}
	}
	return out
}

// LoginTargets reads one SDK directory and each applicable file once. Directory
// failure must be observable by the client that holds the last successful list.
type LoginTarget struct {
	ID, Index string
	Principal LoginPrincipal
	Snapshot  LoginSnapshot
	Disabled  bool
	Reason    error
}

func (m *Manager) LoginTargets() ([]LoginTarget, error) {
	entries, err := m.entries()
	if err != nil {
		return nil, err
	}
	// The execution lookup accepts the public ID, auth index and file name.
	// An ambiguous alias must also be shown as unavailable for editing.
	aliases := make(map[string]int, len(entries))
	for _, entry := range entries {
		aliases[entry.ID]++
		if entry.Index != entry.ID {
			aliases[entry.Index]++
		}
		if entry.Name != entry.Index && entry.Name != entry.ID {
			aliases[entry.Name]++
		}
	}
	out := make([]LoginTarget, 0)
	for _, entry := range entries {
		provider := entry.Provider
		if provider == "" {
			provider = entry.Type
		}
		if entry.RuntimeOnly || provider != "" && provider != "codex" {
			continue
		}
		current, err := m.read(entry)
		if err != nil {
			return nil, err
		}
		if current.Provider != "codex" {
			continue
		}
		snapshot, principal, reason := snapshotAndPrincipal(current)
		if aliases[current.ID] > 1 {
			snapshot = LoginSnapshot{}
			reason = errors.New("identity_conflict")
		}
		out = append(out, LoginTarget{ID: current.ID, Index: current.Index, Principal: principal, Snapshot: snapshot, Disabled: current.Disabled, Reason: reason})
	}
	return out, nil
}
