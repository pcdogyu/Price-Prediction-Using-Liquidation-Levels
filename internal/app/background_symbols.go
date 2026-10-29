package app

import (
	"context"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

const BackgroundCandidateLimit = 50

const backgroundCandidateLimit = BackgroundCandidateLimit

// BackgroundCandidateLimitForProvider keeps the provider package decoupled
// from the Service's internal candidate state while sharing the fixed policy.
func BackgroundCandidateLimitForProvider() int { return BackgroundCandidateLimit }

// BackgroundCandidateProvider builds the next candidate set from the current
// tenant configurations. Implementations must not expose credentials.
type BackgroundCandidateProvider interface {
	RefreshBackgroundCandidates(context.Context) (domain.BackgroundSymbolSnapshot, error)
}

var backgroundCandidateLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

func (s *Service) SetBackgroundCandidateProvider(provider BackgroundCandidateProvider) {
	if s == nil {
		return
	}
	s.candidateMu.Lock()
	s.candidateProvider = provider
	s.candidateMu.Unlock()
}

func (s *Service) loadBackgroundCandidateSnapshot(ctx context.Context) {
	fallback := s.fallbackBackgroundCandidateSnapshot()
	snapshot, found, err := s.store.LoadBackgroundSymbolSnapshot(ctx)
	if err != nil {
		s.log.Warn("load background symbol snapshot failed", "error", err)
	} else if found && validBackgroundCandidateSnapshot(snapshot) {
		s.applyBackgroundCandidateSnapshot(snapshot, false)
		s.log.Info("background symbol snapshot loaded", "symbols", len(snapshot.Symbols), "refreshed_at", snapshot.RefreshedAt)
		return
	}
	s.applyBackgroundCandidateSnapshot(fallback, false)
	s.log.Info("using fallback background symbols until scheduled tenant refresh", "symbols", len(fallback.Symbols))
}

func (s *Service) fallbackBackgroundCandidateSnapshot() domain.BackgroundSymbolSnapshot {
	seen := make(map[string]struct{})
	rows := make([]domain.WatchSymbol, 0)
	add := func(raw string, limited bool) {
		symbol := strings.ToUpper(strings.TrimSpace(raw))
		if symbol == "" || !strings.HasSuffix(symbol, "USDT") || symbol == "USDT" {
			return
		}
		if limited && len(rows) >= backgroundCandidateLimit {
			return
		}
		if _, ok := seen[symbol]; ok {
			return
		}
		seen[symbol] = struct{}{}
		rows = append(rows, domain.WatchSymbol{Symbol: symbol, Rank: len(rows) + 1})
	}
	bootstrap := s.cfg.BootstrapSymbols
	if len(bootstrap) == 0 {
		bootstrap = s.cfg.Symbols
	}
	for _, symbol := range bootstrap {
		add(symbol, true)
	}
	baseCount := len(rows)
	add("BTCUSDT", false)
	add("ETHUSDT", false)
	return domain.BackgroundSymbolSnapshot{Symbols: rows, BaseCount: baseCount, ExceptionSymbols: []string{"BTCUSDT", "ETHUSDT"}}
}

func validBackgroundCandidateSnapshot(snapshot domain.BackgroundSymbolSnapshot) bool {
	if len(snapshot.Symbols) == 0 || len(snapshot.Symbols) > backgroundCandidateLimit+len(snapshot.ExceptionSymbols) {
		return false
	}
	seen := make(map[string]struct{}, len(snapshot.Symbols))
	for _, row := range snapshot.Symbols {
		symbol := strings.ToUpper(strings.TrimSpace(row.Symbol))
		if symbol == "" || !strings.HasSuffix(symbol, "USDT") || symbol == "USDT" {
			return false
		}
		if _, ok := seen[symbol]; ok {
			return false
		}
		seen[symbol] = struct{}{}
	}
	return true
}

func (s *Service) applyBackgroundCandidateSnapshot(snapshot domain.BackgroundSymbolSnapshot, persist bool) {
	if !validBackgroundCandidateSnapshot(snapshot) {
		return
	}
	copySnapshot := snapshot
	copySnapshot.Symbols = append([]domain.WatchSymbol(nil), snapshot.Symbols...)
	copySnapshot.ExceptionSymbols = append([]string(nil), snapshot.ExceptionSymbols...)
	for i := range copySnapshot.Symbols {
		copySnapshot.Symbols[i].Symbol = strings.ToUpper(strings.TrimSpace(copySnapshot.Symbols[i].Symbol))
		copySnapshot.Symbols[i].UpdatedAt = copySnapshot.RefreshedAt
	}
	s.candidateMu.Lock()
	s.candidateSnapshot = copySnapshot
	s.candidates = make([]string, 0, len(copySnapshot.Symbols))
	for _, row := range copySnapshot.Symbols {
		s.candidates = append(s.candidates, row.Symbol)
	}
	updates := s.candidateUpdates
	current := append([]string(nil), s.candidates...)
	s.candidateMu.Unlock()
	if persist {
		if err := s.store.SaveBackgroundSymbolSnapshot(context.Background(), copySnapshot); err != nil {
			s.log.Warn("save background symbol snapshot failed", "error", err)
		}
	}
	if updates != nil {
		select {
		case updates <- current:
		default:
			select {
			case <-updates:
			default:
			}
			select {
			case updates <- current:
			default:
			}
		}
	}
	if s.alerts != nil {
		rows := append([]domain.WatchSymbol(nil), copySnapshot.Symbols...)
		s.alerts.applyWatch(rowsToEntries(rows), rows, copySnapshot.RefreshedAt, "")
	}
	s.log.Info("background symbols applied", "symbols", len(copySnapshot.Symbols), "base", copySnapshot.BaseCount, "exceptions", len(copySnapshot.ExceptionSymbols), "tenants", copySnapshot.TenantCount)
}

func (s *Service) backgroundSymbols() []string {
	s.candidateMu.RLock()
	current := append([]string(nil), s.candidates...)
	s.candidateMu.RUnlock()
	if len(current) > 0 {
		return current
	}
	fallback := s.fallbackBackgroundCandidateSnapshot()
	if len(fallback.Symbols) > 0 {
		current = make([]string, 0, len(fallback.Symbols))
		for _, row := range fallback.Symbols {
			current = append(current, row.Symbol)
		}
		return current
	}
	return []string{"BTCUSDT", "ETHUSDT"}
}

func (s *Service) backgroundCandidateSnapshot() domain.BackgroundSymbolSnapshot {
	s.candidateMu.RLock()
	defer s.candidateMu.RUnlock()
	snapshot := s.candidateSnapshot
	snapshot.Symbols = append([]domain.WatchSymbol(nil), snapshot.Symbols...)
	snapshot.ExceptionSymbols = append([]string(nil), snapshot.ExceptionSymbols...)
	return snapshot
}

func (s *Service) backgroundCandidateRefreshLoop(ctx context.Context) {
	for {
		next := nextBackgroundCandidateRefresh(time.Now())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.refreshBackgroundCandidates(ctx)
		}
	}
}

func nextBackgroundCandidateRefresh(now time.Time) time.Time {
	local := now.In(backgroundCandidateLocation)
	next := time.Date(local.Year(), local.Month(), local.Day(), 7, 50, 0, 0, backgroundCandidateLocation)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (s *Service) refreshBackgroundCandidates(ctx context.Context) {
	s.candidateMu.RLock()
	provider := s.candidateProvider
	s.candidateMu.RUnlock()
	if provider == nil {
		return
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	snapshot, err := provider.RefreshBackgroundCandidates(refreshCtx)
	if err != nil {
		s.log.Warn("background symbol refresh failed; keeping previous snapshot", "error", err)
		return
	}
	if snapshot.RefreshedAt.IsZero() {
		snapshot.RefreshedAt = time.Now().UTC()
	}
	if !validBackgroundCandidateSnapshot(snapshot) {
		s.log.Warn("background symbol refresh returned invalid snapshot", "symbols", len(snapshot.Symbols))
		return
	}
	s.applyBackgroundCandidateSnapshot(snapshot, true)
	// Newly selected symbols may not have the current history window yet. Run
	// the same gap-aware backfill used at startup against the captured set so a
	// later candidate refresh cannot change the work midway through the pass.
	symbols := s.bootstrapSymbols()
	go func() {
		complete := s.backfillSymbols(ctx, symbols)
		s.mu.Lock()
		s.backfillDone = complete
		s.mu.Unlock()
		if ctx.Err() == nil {
			s.rebuild(ctx)
		}
	}()
}

func (s *Service) candidateWatchRows() []domain.WatchSymbol {
	snapshot := s.backgroundCandidateSnapshot()
	if len(snapshot.Symbols) > 0 {
		return snapshot.Symbols
	}
	return nil
}

func backgroundCandidateCount(snapshot domain.BackgroundSymbolSnapshot) (int, int) {
	base := snapshot.BaseCount
	if base <= 0 || base > len(snapshot.Symbols) {
		base = len(snapshot.Symbols)
	}
	return base, len(snapshot.Symbols) - base
}
