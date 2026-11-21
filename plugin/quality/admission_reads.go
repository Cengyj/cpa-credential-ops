package quality

import (
	"context"
	"fmt"
)

// A slow authority read must not write into a changed rule, configuration or
// credential login. Policy fields are checked again after reacquiring the lock.
type admissionFence struct {
	settings                    Config
	login, scheduling, revision uint64
	rule                        *Rule
}

func (en *Engine) admissionFenceLocked(rule *Rule) admissionFence {
	f := admissionFence{settings: en.cfg, login: en.loginGeneration, scheduling: en.scheduleGeneration, rule: rule}
	if rule != nil {
		f.revision = rule.Revision
	}
	return f
}

func (en *Engine) checkAdmissionFenceLocked(ctx context.Context, f admissionFence, ids []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := en.mutableLocked(); err != nil {
		return err
	}
	if en.cfg != f.settings || en.scheduleGeneration != f.scheduling || f.rule != nil && (en.rules[f.rule.ID] != f.rule || f.rule.Revision != f.revision) {
		return fmt.Errorf("conflict: rule or configuration changed during CPA verification")
	}
	for _, id := range ids {
		if hold := en.loginHolds[id]; hold != nil && hold.generation > f.login {
			return fmt.Errorf("conflict: credential login changed during CPA verification")
		}
	}
	return nil
}

func admissionReadLocked[T any](en *Engine, ctx context.Context, config RunConfig, rule *Rule, read func() (T, error)) (T, error) {
	f := en.admissionFenceLocked(rule)
	ids := dependencies(config)
	owners := make([]*CredentialDisposition, len(ids))
	for i, id := range ids {
		owners[i] = en.managed[id]
	}
	en.mu.Unlock()
	result, err := read()
	en.mu.Lock()
	if err == nil {
		err = en.checkAdmissionFenceLocked(ctx, f, ids)
	}
	if err == nil {
		for i, id := range ids {
			if en.managed[id] != owners[i] {
				err = fmt.Errorf("conflict: credential handling changed during CPA verification")
				break
			}
		}
	}
	if err != nil {
		var zero T
		return zero, err
	}
	return result, nil
}

func (en *Engine) readAdmissionTargetsLocked(ctx context.Context, read func(context.Context) ([]Target, error), config RunConfig, rule *Rule) ([]Target, error) {
	return admissionReadLocked(en, ctx, config, rule, func() ([]Target, error) { return read(ctx) })
}
