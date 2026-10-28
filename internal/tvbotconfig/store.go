package config

import (
	"sync"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

type Store struct {
	mu   sync.RWMutex
	path string
	cfg  Config
}

func NewStore(path string, cfg Config) *Store {
	cfg.Normalize()
	return &Store{path: path, cfg: cfg}
}

func (s *Store) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cfg := s.cfg
	cfg.Trading.IgnoredCoinpairs = cloneStringList(s.cfg.Trading.IgnoredCoinpairs)
	cfg.Trading.CoinpairWhitelist = cloneStringList(s.cfg.Trading.CoinpairWhitelist)
	cfg.Trading.LayeredOrder.PriceLayers = cloneLayeredOrderLayers(s.cfg.Trading.LayeredOrder.PriceLayers)
	cfg.Symbols = cloneSymbols(s.cfg.Symbols)
	cfg.UI.MenuItems = cloneMenuItems(s.cfg.UI.MenuItems)
	cfg.UI.TableColumns = cloneTableColumns(s.cfg.UI.TableColumns)
	return cfg
}

func (s *Store) Update(fn func(*Config) error) (Config, error) {
	return s.update(fn, true)
}

// UpdateSettings saves settings that do not replace the symbol catalog. It
// tolerates incomplete legacy symbol entries so an unrelated setting can be
// repaired or changed before the catalog itself is fixed through Update.
func (s *Store) UpdateSettings(fn func(*Config) error) (Config, error) {
	return s.update(fn, false)
}

func (s *Store) update(fn func(*Config) error, validateSymbols bool) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg
	next.Trading.IgnoredCoinpairs = cloneStringList(s.cfg.Trading.IgnoredCoinpairs)
	next.Trading.CoinpairWhitelist = cloneStringList(s.cfg.Trading.CoinpairWhitelist)
	next.Trading.LayeredOrder.PriceLayers = cloneLayeredOrderLayers(s.cfg.Trading.LayeredOrder.PriceLayers)
	next.Symbols = cloneSymbols(s.cfg.Symbols)
	next.UI.MenuItems = cloneMenuItems(s.cfg.UI.MenuItems)
	next.UI.TableColumns = cloneTableColumns(s.cfg.UI.TableColumns)
	if err := fn(&next); err != nil {
		return Config{}, err
	}
	next.Normalize()
	if err := next.validate(validateSymbols); err != nil {
		return Config{}, err
	}
	if s.path != "" {
		if err := Save(s.path, next); err != nil {
			return Config{}, err
		}
	}
	s.cfg = next
	return next, nil
}

func cloneSymbols(in map[string]SymbolConfig) map[string]SymbolConfig {
	out := make(map[string]SymbolConfig, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringList(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func cloneLayeredOrderLayers(in []trading.LayeredOrderLayer) []trading.LayeredOrderLayer {
	if in == nil {
		return nil
	}
	out := make([]trading.LayeredOrderLayer, len(in))
	copy(out, in)
	return out
}

func cloneMenuItems(in []MenuItemConfig) []MenuItemConfig {
	if len(in) == 0 {
		return nil
	}
	out := make([]MenuItemConfig, len(in))
	copy(out, in)
	return out
}

func cloneTableColumns(in TableColumnsConfig) TableColumnsConfig {
	return TableColumnsConfig{
		Positions:     cloneStrings(in.Positions),
		PendingOrders: cloneStrings(in.PendingOrders),
		Symbols:       cloneStrings(in.Symbols),
	}
}
