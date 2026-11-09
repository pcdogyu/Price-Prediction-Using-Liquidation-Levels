package trading

import (
	"errors"
	"fmt"
	"math"
)

const (
	OrderAmountModeFixedUSDT = "fixed_usdt"
	OrderAmountModeEquityPct = "equity_pct"
)

type PyramidOrderSettings struct {
	Enabled              bool      `json:"enabled"`
	MaxAdditionalEntries int       `json:"max_additional_entries"`
	AddAmountPcts        []float64 `json:"add_amount_pcts"`
	MinFavorableMovePct  float64   `json:"min_favorable_move_pct"`
}

func DefaultPyramidOrderSettings() PyramidOrderSettings {
	return PyramidOrderSettings{
		Enabled:              false,
		MaxAdditionalEntries: 3,
		AddAmountPcts:        []float64{5, 10, 15},
		MinFavorableMovePct:  1,
	}
}

func (s PyramidOrderSettings) Normalize() PyramidOrderSettings {
	out := s
	if out.MaxAdditionalEntries < 0 {
		out.MaxAdditionalEntries = 0
	}
	if !out.Enabled && out.MaxAdditionalEntries == 0 && len(out.AddAmountPcts) == 0 && out.MinFavorableMovePct <= 0 {
		defaults := DefaultPyramidOrderSettings()
		out.MaxAdditionalEntries = defaults.MaxAdditionalEntries
		out.AddAmountPcts = append([]float64(nil), defaults.AddAmountPcts...)
	}
	if len(out.AddAmountPcts) == 0 {
		out.AddAmountPcts = make([]float64, out.MaxAdditionalEntries)
		for i := range out.AddAmountPcts {
			out.AddAmountPcts[i] = 5
		}
	}
	if out.MinFavorableMovePct < 0 {
		out.MinFavorableMovePct = 1
	}
	out.AddAmountPcts = append([]float64(nil), out.AddAmountPcts...)
	return out
}

func (s PyramidOrderSettings) Validate() error {
	if s.MaxAdditionalEntries < 0 || s.MaxAdditionalEntries > 20 {
		return errors.New("pyramid_order.max_additional_entries must be between 0 and 20")
	}
	if len(s.AddAmountPcts) != s.MaxAdditionalEntries {
		return fmt.Errorf("pyramid_order.add_amount_pcts must contain exactly %d entries", s.MaxAdditionalEntries)
	}
	for i, value := range s.AddAmountPcts {
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > 100 {
			return fmt.Errorf("pyramid_order.add_amount_pcts[%d] must be between 0 and 100", i)
		}
	}
	if math.IsNaN(s.MinFavorableMovePct) || math.IsInf(s.MinFavorableMovePct, 0) || s.MinFavorableMovePct < 0 || s.MinFavorableMovePct > 100 {
		return errors.New("pyramid_order.min_favorable_move_pct must be between 0 and 100")
	}
	return nil
}

func ValidOrderAmountMode(mode string) bool {
	return mode == OrderAmountModeFixedUSDT || mode == OrderAmountModeEquityPct
}

func ResolveOrderAmount(mode string, fixedUSDT, equityPct, totalEquity float64) (float64, bool) {
	if mode == OrderAmountModeEquityPct && totalEquity > 0 && equityPct > 0 {
		return totalEquity * equityPct / 100, true
	}
	if fixedUSDT > 0 {
		return fixedUSDT, false
	}
	return 0, false
}

func FavorablePyramidPrice(action Side, signalPrice, averagePrice, minimumMovePct float64) bool {
	if signalPrice <= 0 || averagePrice <= 0 || minimumMovePct < 0 {
		return false
	}
	if action == ActionShort {
		return signalPrice <= averagePrice*(1-minimumMovePct/100)
	}
	return signalPrice >= averagePrice*(1+minimumMovePct/100)
}
