package storage

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

type CoinpairBlock struct {
	Keyword       string    `json:"keyword"`
	Symbol        string    `json:"symbol"`
	TriggerPrice  string    `json:"trigger_price,omitempty"`
	Source        string    `json:"source"`
	Exchange      string    `json:"exchange,omitempty"`
	APIID         string    `json:"api_id,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	LastEventID   string    `json:"-"`
	RemainingSecs int64     `json:"remaining_seconds"`
}

type CoinpairBlockEvent struct {
	EventID      string
	Keyword      string
	Symbol       string
	TriggerPrice string
	Source       string
	Exchange     string
	APIID        string
	OccurredAt   time.Time
	ExpiresAt    time.Time
}

func (event *CoinpairBlockEvent) normalize() {
	event.EventID = strings.TrimSpace(event.EventID)
	event.Keyword = strings.ToUpper(strings.TrimSpace(event.Keyword))
	event.Symbol = strings.ToUpper(strings.TrimSpace(event.Symbol))
	event.TriggerPrice = strings.TrimSpace(event.TriggerPrice)
	event.Source = strings.ToLower(strings.TrimSpace(event.Source))
	if strings.TrimSpace(event.Exchange) != "" {
		event.Exchange = trading.NormalizeExchange(event.Exchange)
	}
	event.APIID = strings.TrimSpace(event.APIID)
	event.OccurredAt = event.OccurredAt.UTC()
	event.ExpiresAt = event.ExpiresAt.UTC()
}

func (s *OrderStore) AddCoinpairBlockEvent(event CoinpairBlockEvent, now time.Time) (CoinpairBlock, bool, error) {
	event.normalize()
	now = now.UTC()
	if event.EventID == "" || event.Keyword == "" || event.OccurredAt.IsZero() || event.ExpiresAt.IsZero() {
		return CoinpairBlock{}, false, errors.New("coinpair block event requires event_id, keyword, occurred_at and expires_at")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.addCoinpairBlockEventSQLiteLocked(event, now)
	}
	if s.state.CoinpairBlockEvents == nil {
		s.state.CoinpairBlockEvents = map[string]bool{}
	}
	if s.state.CoinpairBlockEvents[event.EventID] {
		block, _ := activeMemoryCoinpairBlock(s.state.CoinpairBlocks, event.Keyword, now)
		return block, false, nil
	}
	s.state.CoinpairBlockEvents[event.EventID] = true
	if !event.ExpiresAt.After(now) {
		return CoinpairBlock{}, false, s.saveLocked()
	}
	block := coinpairBlockFromEvent(event, now)
	found := false
	for i := range s.state.CoinpairBlocks {
		if s.state.CoinpairBlocks[i].Keyword != event.Keyword {
			continue
		}
		found = true
		if !event.OccurredAt.Before(s.state.CoinpairBlocks[i].StartedAt) {
			s.state.CoinpairBlocks[i] = block
		} else {
			block = s.state.CoinpairBlocks[i]
		}
		break
	}
	if !found {
		s.state.CoinpairBlocks = append(s.state.CoinpairBlocks, block)
	}
	if err := s.saveLocked(); err != nil {
		return CoinpairBlock{}, false, err
	}
	return blockWithRemaining(block, now), true, nil
}

func (s *OrderStore) addCoinpairBlockEventSQLiteLocked(event CoinpairBlockEvent, now time.Time) (CoinpairBlock, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return CoinpairBlock{}, false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT OR IGNORE INTO coinpair_block_events
		(event_id, keyword, occurred_at, expires_at, created_at) VALUES (?, ?, ?, ?, ?)`,
		event.EventID,
		event.Keyword,
		event.OccurredAt.Format(time.RFC3339Nano),
		event.ExpiresAt.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return CoinpairBlock{}, false, err
	}
	inserted, _ := res.RowsAffected()
	if inserted == 0 {
		block, found, err := findCoinpairBlockSQLite(tx, event.Keyword)
		if err != nil {
			return CoinpairBlock{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return CoinpairBlock{}, false, err
		}
		if !found || !block.ExpiresAt.After(now) {
			return CoinpairBlock{}, false, nil
		}
		return blockWithRemaining(block, now), false, nil
	}
	if !event.ExpiresAt.After(now) {
		if err := tx.Commit(); err != nil {
			return CoinpairBlock{}, false, err
		}
		return CoinpairBlock{}, false, nil
	}
	_, err = tx.Exec(`INSERT INTO coinpair_blocks
		(keyword, symbol, trigger_price, source, exchange, api_id, started_at, expires_at, last_event_id, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(keyword) DO UPDATE SET
			symbol=excluded.symbol,
			trigger_price=excluded.trigger_price,
			source=excluded.source,
			exchange=excluded.exchange,
			api_id=excluded.api_id,
			started_at=excluded.started_at,
			expires_at=excluded.expires_at,
			last_event_id=excluded.last_event_id,
			updated_at=excluded.updated_at
		WHERE excluded.started_at >= coinpair_blocks.started_at`,
		event.Keyword,
		event.Symbol,
		event.TriggerPrice,
		event.Source,
		event.Exchange,
		event.APIID,
		event.OccurredAt.Format(time.RFC3339Nano),
		event.ExpiresAt.Format(time.RFC3339Nano),
		event.EventID,
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return CoinpairBlock{}, false, err
	}
	block, found, err := findCoinpairBlockSQLite(tx, event.Keyword)
	if err != nil {
		return CoinpairBlock{}, false, err
	}
	if !found {
		return CoinpairBlock{}, false, errors.New("coinpair block was not saved")
	}
	if err := tx.Commit(); err != nil {
		return CoinpairBlock{}, false, err
	}
	return blockWithRemaining(block, now), true, nil
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

func findCoinpairBlockSQLite(q queryRower, keyword string) (CoinpairBlock, bool, error) {
	row := q.QueryRow(`SELECT keyword, symbol, trigger_price, source, exchange, api_id, started_at, expires_at, last_event_id
		FROM coinpair_blocks WHERE keyword = ?`, keyword)
	block, err := scanCoinpairBlock(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return CoinpairBlock{}, false, nil
	}
	return block, err == nil, err
}

func scanCoinpairBlock(scan func(dest ...any) error) (CoinpairBlock, error) {
	var block CoinpairBlock
	var startedAt, expiresAt string
	err := scan(
		&block.Keyword,
		&block.Symbol,
		&block.TriggerPrice,
		&block.Source,
		&block.Exchange,
		&block.APIID,
		&startedAt,
		&expiresAt,
		&block.LastEventID,
	)
	if err != nil {
		return CoinpairBlock{}, err
	}
	block.StartedAt = parseStoreTime(startedAt)
	block.ExpiresAt = parseStoreTime(expiresAt)
	return block, nil
}

func (s *OrderStore) ListActiveCoinpairBlocks(now time.Time) ([]CoinpairBlock, error) {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		if _, err := s.deleteExpiredCoinpairBlocksSQLiteLocked(now); err != nil {
			return nil, err
		}
		rows, err := s.db.Query(`SELECT keyword, symbol, trigger_price, source, exchange, api_id, started_at, expires_at, last_event_id
			FROM coinpair_blocks WHERE expires_at > ? ORDER BY expires_at ASC, keyword ASC`, now.Format(time.RFC3339Nano))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []CoinpairBlock{}
		for rows.Next() {
			block, err := scanCoinpairBlock(rows.Scan)
			if err != nil {
				return nil, err
			}
			out = append(out, blockWithRemaining(block, now))
		}
		return out, rows.Err()
	}
	changed := pruneMemoryCoinpairBlocks(&s.state, now)
	if changed {
		if err := s.saveLocked(); err != nil {
			return nil, err
		}
	}
	out := make([]CoinpairBlock, 0, len(s.state.CoinpairBlocks))
	for _, block := range s.state.CoinpairBlocks {
		out = append(out, blockWithRemaining(block, now))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ExpiresAt.Equal(out[j].ExpiresAt) {
			return out[i].Keyword < out[j].Keyword
		}
		return out[i].ExpiresAt.Before(out[j].ExpiresAt)
	})
	return out, nil
}

func (s *OrderStore) DeleteExpiredCoinpairBlocks(now time.Time) (int, error) {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.deleteExpiredCoinpairBlocksSQLiteLocked(now)
	}
	before := len(s.state.CoinpairBlocks)
	if !pruneMemoryCoinpairBlocks(&s.state, now) {
		return 0, nil
	}
	if err := s.saveLocked(); err != nil {
		return 0, err
	}
	return before - len(s.state.CoinpairBlocks), nil
}

func (s *OrderStore) DeleteCoinpairBlock(keyword string) (bool, error) {
	keyword = strings.ToUpper(strings.TrimSpace(keyword))
	if keyword == "" {
		return false, errors.New("coinpair block keyword is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		res, err := s.db.Exec(`DELETE FROM coinpair_blocks WHERE keyword = ?`, keyword)
		if err != nil {
			return false, err
		}
		count, _ := res.RowsAffected()
		return count > 0, nil
	}
	out := s.state.CoinpairBlocks[:0]
	removed := false
	for _, block := range s.state.CoinpairBlocks {
		if block.Keyword == keyword {
			removed = true
			continue
		}
		out = append(out, block)
	}
	if !removed {
		return false, nil
	}
	s.state.CoinpairBlocks = out
	if err := s.saveLocked(); err != nil {
		return false, err
	}
	return true, nil
}

// DeleteAutomaticCoinpairBlocks clears active system-generated cooldowns but
// leaves analyst-created cooldowns intact. Event history is deliberately kept
// so repeated fill polls remain idempotent.
func (s *OrderStore) DeleteAutomaticCoinpairBlocks() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		res, err := s.db.Exec(`DELETE FROM coinpair_blocks WHERE source <> 'analysis_manual'`)
		if err != nil {
			return 0, err
		}
		count, _ := res.RowsAffected()
		return int(count), nil
	}
	before := len(s.state.CoinpairBlocks)
	out := s.state.CoinpairBlocks[:0]
	for _, block := range s.state.CoinpairBlocks {
		if block.Source == "analysis_manual" {
			out = append(out, block)
		}
	}
	if len(out) == before {
		return 0, nil
	}
	s.state.CoinpairBlocks = out
	if err := s.saveLocked(); err != nil {
		return 0, err
	}
	return before - len(out), nil
}

// DeleteAutomaticCoinpairBlocksExcept clears active system-generated
// cooldowns except for one reserved scope, preserving that scope when the
// other automatic cooldown setting is disabled.
func (s *OrderStore) DeleteAutomaticCoinpairBlocksExcept(keyword string) (int, error) {
	keyword = strings.ToUpper(strings.TrimSpace(keyword))
	if keyword == "" {
		return s.DeleteAutomaticCoinpairBlocks()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		res, err := s.db.Exec(`DELETE FROM coinpair_blocks WHERE source <> 'analysis_manual' AND keyword <> ?`, keyword)
		if err != nil {
			return 0, err
		}
		count, _ := res.RowsAffected()
		return int(count), nil
	}
	before := len(s.state.CoinpairBlocks)
	out := s.state.CoinpairBlocks[:0]
	for _, block := range s.state.CoinpairBlocks {
		if block.Source != "analysis_manual" && block.Keyword != keyword {
			continue
		}
		out = append(out, block)
	}
	if len(out) == before {
		return 0, nil
	}
	s.state.CoinpairBlocks = out
	if err := s.saveLocked(); err != nil {
		return 0, err
	}
	return before - len(out), nil
}

func (s *OrderStore) deleteExpiredCoinpairBlocksSQLiteLocked(now time.Time) (int, error) {
	res, err := s.db.Exec(`DELETE FROM coinpair_blocks WHERE expires_at <= ?`, now.Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	count, _ := res.RowsAffected()
	return int(count), nil
}

func coinpairBlockFromEvent(event CoinpairBlockEvent, now time.Time) CoinpairBlock {
	return blockWithRemaining(CoinpairBlock{
		Keyword:      event.Keyword,
		Symbol:       event.Symbol,
		TriggerPrice: event.TriggerPrice,
		Source:       event.Source,
		Exchange:     event.Exchange,
		APIID:        event.APIID,
		StartedAt:    event.OccurredAt,
		ExpiresAt:    event.ExpiresAt,
		LastEventID:  event.EventID,
	}, now)
}

func blockWithRemaining(block CoinpairBlock, now time.Time) CoinpairBlock {
	remaining := block.ExpiresAt.Sub(now.UTC())
	if remaining <= 0 {
		block.RemainingSecs = 0
	} else {
		block.RemainingSecs = int64(remaining / time.Second)
		if remaining%time.Second != 0 {
			block.RemainingSecs++
		}
	}
	return block
}

func activeMemoryCoinpairBlock(blocks []CoinpairBlock, keyword string, now time.Time) (CoinpairBlock, bool) {
	for _, block := range blocks {
		if block.Keyword == keyword && block.ExpiresAt.After(now) {
			return blockWithRemaining(block, now), true
		}
	}
	return CoinpairBlock{}, false
}

func pruneMemoryCoinpairBlocks(state *orderState, now time.Time) bool {
	out := state.CoinpairBlocks[:0]
	for _, block := range state.CoinpairBlocks {
		if block.ExpiresAt.After(now) {
			out = append(out, block)
		}
	}
	changed := len(out) != len(state.CoinpairBlocks)
	state.CoinpairBlocks = out
	return changed
}
