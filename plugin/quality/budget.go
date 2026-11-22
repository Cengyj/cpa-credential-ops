package quality

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"

	bolt "go.etcd.io/bbolt"
)

var errTokenBudgetExhausted = errors.New("token_budget_exhausted")

// reserve binds accounting to the UTC admission date, including calls that finish after midnight.
func (en *Engine) reserve(maxTokens int) (string, error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	if err := en.mutableLocked(); err != nil {
		return "", err
	}
	day := en.now().UTC().Format("2006-01-02")
	next := en.budget
	if next.Day != day {
		next = Budget{Day: day}
		if err := en.store.Get("meta", "budget:"+day, &next); err != nil && !errors.Is(err, os.ErrNotExist) {
			en.persistFailure(err)
			return "", fmt.Errorf("read daily budget: %w", err)
		}
	}
	used := max(next.ReservedTokens, next.KnownTokens)
	if maxTokens < 1 || used > en.cfg.DailyTokens || int64(maxTokens) > en.cfg.DailyTokens-used {
		return "", errTokenBudgetExhausted
	}
	// Long rounds can reach the configured storage threshold after acceptance.
	// Leave completed samples and final bookkeeping writable at that point.
	if err := en.storageAdmission(); err != nil {
		return "", err
	}
	next.Calls++
	next.ReservedTokens += int64(maxTokens)
	err := en.store.Write(func(tx *bolt.Tx) error {
		// Preserve the legacy active record when the first rollover upgrades an existing store.
		if en.budget.Day != "" && en.budget.Day != day {
			if err := put(tx, "meta", "budget:"+en.budget.Day, en.budget); err != nil {
				return err
			}
		}
		if err := put(tx, "meta", "budget:"+day, next); err != nil {
			return err
		}
		if err := put(tx, "meta", "budget", next); err != nil {
			return err
		}
		return en.pruneBudgetDaysLocked(tx, day, "")
	})
	if err != nil {
		en.persistFailure(err)
		return "", fmt.Errorf("persist budget reservation: %w", err)
	}
	en.budget = next
	if en.budgetPending == nil {
		en.budgetPending = map[string]int{}
	}
	en.budgetPending[day]++
	return day, nil
}

func (en *Engine) account(day string, usage *Usage) error {
	en.mu.Lock()
	defer en.mu.Unlock()
	next := en.budget
	if next.Day != day {
		if err := en.store.Get("meta", "budget:"+day, &next); err != nil {
			en.persistFailure(err)
			return fmt.Errorf("read reserved budget day: %w", err)
		}
	}
	if usage == nil || usage.Total < 0 {
		next.UnknownUsage++
	} else {
		if usage.Total > math.MaxInt64-next.KnownTokens {
			next.KnownTokens = math.MaxInt64
		} else {
			next.KnownTokens += usage.Total
		}
	}
	err := en.store.Write(func(tx *bolt.Tx) error {
		if err := put(tx, "meta", "budget:"+day, next); err != nil {
			return err
		}
		if en.budget.Day == day {
			if err := put(tx, "meta", "budget", next); err != nil {
				return err
			}
		}
		return en.pruneBudgetDaysLocked(tx, en.budget.Day, day)
	})
	if err != nil {
		en.persistFailure(err)
		return fmt.Errorf("persist budget usage: %w", err)
	}
	if en.budget.Day == day {
		en.budget = next
	}
	if en.budgetPending[day] <= 1 {
		delete(en.budgetPending, day)
	} else {
		en.budgetPending[day]--
	}
	return nil
}

// pruneBudgetDaysLocked retains configured history and every day with an unfinished call.
func (en *Engine) pruneBudgetDaysLocked(tx *bolt.Tx, activeDay, completedDay string) error {
	cutoff := en.now().UTC().AddDate(0, 0, -en.cfg.RetentionDays+1).Format("2006-01-02")
	prefix := []byte("budget:")
	metadata := tx.Bucket([]byte("meta"))
	cursor := metadata.Cursor()
	var expired []string
	for key, _ := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, _ = cursor.Next() {
		day := string(key[len(prefix):])
		pending := en.budgetPending[day]
		if day == completedDay {
			pending--
		}
		if day < cutoff && day != activeDay && pending <= 0 {
			expired = append(expired, string(key))
		}
	}
	for _, key := range expired {
		if err := metadata.Delete([]byte(key)); err != nil {
			return err
		}
	}
	return nil
}
