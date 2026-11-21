package quality

import (
	"fmt"
	"math"
	"net/url"
	"sort"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

type auditPosition struct {
	key, id string
	when    time.Time
}

// The index contains only sort keys. The immutable positions and returned rows
// come from the same bbolt read transaction, including immediately after prune.
type auditIndex struct {
	mu        sync.Mutex
	revision  int
	positions []auditPosition
}

func (s *Store) historyPage(query url.Values) (map[string]any, error) {
	offset, limit := pageBounds(query)
	items, total, err := s.auditWindow(offset, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "total": total, "offset": min(offset, total), "limit": limit}, nil
}

func (en *Engine) History() ([]Audit, error) {
	rows, _, err := en.store.auditWindow(0, math.MaxInt)
	return rows, err
}

func (s *Store) auditWindow(offset, limit int) ([]Audit, int, error) {
	index := &s.history
	index.mu.Lock()
	defer index.mu.Unlock()
	var rows []Audit
	var total int
	err := s.db.View(func(tx *bolt.Tx) error {
		table := tx.Bucket([]byte("audit"))
		if index.revision != tx.ID() {
			var positions []auditPosition
			err := table.ForEach(func(k, v []byte) error {
				var row Audit
				if err := decode(v, &row); err != nil {
					return err
				}
				positions = append(positions, auditPosition{string(k), row.ID, row.Time})
				return nil
			})
			if err != nil {
				return err
			}
			sort.SliceStable(positions, func(i, j int) bool {
				if positions[i].when.Equal(positions[j].when) {
					return positions[i].id < positions[j].id
				}
				return positions[i].when.After(positions[j].when)
			})
			index.revision, index.positions = tx.ID(), positions
		}
		total = len(index.positions)
		offset = min(offset, total)
		rows = make([]Audit, min(limit, total-offset))
		for i, pos := range index.positions[offset : offset+len(rows)] {
			if err := decode(table.Get([]byte(pos.key)), &rows[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("unavailable: read audit history: %w", err)
	}
	return rows, total, nil
}
