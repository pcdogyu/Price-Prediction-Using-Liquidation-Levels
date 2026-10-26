// Package valuearea contains the price-only value-area crossing strategy.
// It deliberately has no persistence or exchange dependencies so the crossing
// and re-arm rules can be tested independently of the alert runtime.
package valuearea

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

type state struct {
	sessionStart time.Time
	val, vah     float64
	previous     float64
	havePrice    bool
	longArmed    bool
	shortArmed   bool
}

// Engine is one independent state machine. The application creates one per
// alert runtime, so tenant history and restart de-duplication stay isolated.
type Engine struct {
	states map[string]state
}

func New() *Engine { return &Engine{states: make(map[string]state)} }

// Process consumes one live price and the latest profile/Gamma snapshots. It
// returns at most one signal. A profile/session/boundary change establishes a
// new baseline and cannot itself create a crossing.
func (e *Engine) Process(tick domain.PriceTick, profile domain.VolumeProfile, gamma domain.AlertGamma) (domain.TradeSignal, bool) {
	if e == nil || !validTick(tick) || !validProfile(profile) || normalizeSymbol(profile.Symbol) != normalizeSymbol(tick.Symbol) || !validGamma(tick.Time, gamma) {
		if e != nil && tick.Symbol != "" {
			delete(e.states, normalizeSymbol(tick.Symbol))
		}
		return domain.TradeSignal{}, false
	}

	symbol := normalizeSymbol(tick.Symbol)
	current := e.states[symbol]
	if !current.havePrice || !sameProfile(current, profile) {
		current = state{
			sessionStart: profile.SessionStart.UTC(), val: *profile.VAL, vah: *profile.VAH,
			previous: tick.Price, havePrice: true,
			longArmed:  tick.Price >= *profile.VAL,
			shortArmed: tick.Price <= *profile.VAH,
		}
		e.states[symbol] = current
		return domain.TradeSignal{}, false
	}

	previous := current.previous
	wasInside := previous >= current.val && previous <= current.vah
	longCross := current.longArmed && wasInside && tick.Price < current.val
	shortCross := current.shortArmed && wasInside && tick.Price > current.vah
	current.previous = tick.Price
	if tick.Price >= current.val {
		current.longArmed = true
	}
	if tick.Price <= current.vah {
		current.shortArmed = true
	}
	if longCross {
		current.longArmed = false
	}
	if shortCross {
		current.shortArmed = false
	}
	e.states[symbol] = current
	if !longCross && !shortCross {
		return domain.TradeSignal{}, false
	}

	boundary, boundaryPrice, side := "val", current.val, "long"
	if shortCross {
		boundary, boundaryPrice, side = "vah", current.vah, "short"
	}
	effect := "close"
	if *gamma.Gamma > 0 {
		effect = "open"
	}
	gammaValue := *gamma.Gamma
	id := signalID(symbol, profile.SessionStart, boundary, tick)
	return domain.TradeSignal{
		ID: id, AlertID: "value-area:" + id, Symbol: symbol, Kind: domain.SignalValueArea,
		Side: side, LiquidationSide: side, CreatedAt: tick.Time.UTC(), ReferencePrice: tick.Price,
		GammaIgnored: effect == "close", OpenAllowed: effect == "open", CloseAllowed: true,
		PositionEffect: effect, Boundary: boundary, BoundaryPrice: boundaryPrice,
		VAL: current.val, VAH: current.vah, GammaValue: &gammaValue, GammaState: gamma.State,
	}, true
}

func validTick(tick domain.PriceTick) bool {
	return strings.TrimSpace(tick.Symbol) != "" && tick.Price > 0 && !math.IsNaN(tick.Price) && !math.IsInf(tick.Price, 0) && !tick.Time.IsZero()
}

func validProfile(profile domain.VolumeProfile) bool {
	return strings.EqualFold(strings.TrimSpace(profile.State), "ok") && profile.VAL != nil && profile.VAH != nil &&
		*profile.VAL > 0 && *profile.VAH >= *profile.VAL && !math.IsNaN(*profile.VAL) && !math.IsNaN(*profile.VAH) &&
		!math.IsInf(*profile.VAL, 0) && !math.IsInf(*profile.VAH, 0) && !profile.SessionStart.IsZero()
}

func validGamma(at time.Time, gamma domain.AlertGamma) bool {
	if gamma.Gamma == nil || math.IsNaN(*gamma.Gamma) || math.IsInf(*gamma.Gamma, 0) {
		return false
	}
	state := strings.ToLower(strings.TrimSpace(gamma.State))
	if state != "ok" && state != "partial" {
		return false
	}
	if gamma.UpdatedAt != nil {
		updated := gamma.UpdatedAt.UTC()
		if updated.After(at.UTC().Add(2*time.Minute)) || at.UTC().Sub(updated) > domain.OptionGammaStaleAfter {
			return false
		}
	}
	return *gamma.Gamma != 0
}

func sameProfile(previous state, profile domain.VolumeProfile) bool {
	return previous.sessionStart.Equal(profile.SessionStart.UTC()) && previous.val == *profile.VAL && previous.vah == *profile.VAH
}

func normalizeSymbol(symbol string) string { return strings.ToUpper(strings.TrimSpace(symbol)) }

func signalID(symbol string, session time.Time, boundary string, tick domain.PriceTick) string {
	key := fmt.Sprintf("value-area|%s|%d|%s|%d", symbol, session.UTC().UnixMilli(), boundary, tick.TradeID)
	if tick.TradeID == 0 {
		key = fmt.Sprintf("value-area|%s|%d|%s|%d", symbol, session.UTC().UnixMilli(), boundary, tick.Time.UTC().UnixNano())
	}
	sum := sha256.Sum256([]byte(key))
	return "va-" + hex.EncodeToString(sum[:])
}
