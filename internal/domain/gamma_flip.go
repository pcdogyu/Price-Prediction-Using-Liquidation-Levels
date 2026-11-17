package domain

import (
	"math"
	"sort"
	"strings"
	"time"
)

const GammaFlipSnapshotMaxAge = 15 * time.Minute

type GammaFlipDecision struct {
	Snapshot  *GammaFlipSnapshot
	Regime    string
	NearFlip  bool
	Confirmed bool
	Available bool
	Reason    string
}

// EvaluateGammaFlip uses only snapshots available at the decision time. A
// regime is confirmed when the requested number of consecutive healthy
// samples have the same non-zero GEX sign and all remain outside the flip
// buffer. The newest sample must also be fresh.
func EvaluateGammaFlip(history []GammaFlipSnapshot, at time.Time, nearPct float64, confirmations int) GammaFlipDecision {
	decision := GammaFlipDecision{Regime: "unknown", Reason: "gamma_flip_unavailable"}
	if at.IsZero() || nearPct <= 0 || math.IsNaN(nearPct) || math.IsInf(nearPct, 0) || confirmations < 1 {
		return decision
	}
	points := append([]GammaFlipSnapshot(nil), history...)
	sort.SliceStable(points, func(i, j int) bool { return points[i].Time.Before(points[j].Time) })
	latest := -1
	for i := range points {
		if !points[i].Time.After(at) {
			latest = i
		}
	}
	if latest < 0 {
		return decision
	}
	current := points[latest]
	if !gammaFlipSnapshotUsable(current) || current.Time.After(at.Add(2*time.Minute)) || at.Sub(current.Time) > GammaFlipSnapshotMaxAge {
		return decision
	}
	decision.Snapshot = &current
	decision.Available = true
	distancePct := math.Abs(current.SpotPrice-*current.GammaFlip) / current.SpotPrice * 100
	if distancePct <= nearPct {
		decision.NearFlip = true
		decision.Reason = "gamma_flip_near"
		return decision
	}
	if current.GammaValue > 0 {
		decision.Regime = "positive"
	} else if current.GammaValue < 0 {
		decision.Regime = "negative"
	} else {
		decision.Reason = "gamma_flip_unconfirmed"
		return decision
	}

	count := 0
	lastAt := time.Time{}
	for i := latest; i >= 0 && count < confirmations; i-- {
		point := points[i]
		if !gammaFlipSnapshotUsable(point) || point.Time.After(at.Add(2*time.Minute)) || at.Sub(point.Time) > GammaFlipSnapshotMaxAge {
			break
		}
		if !lastAt.IsZero() && lastAt.Sub(point.Time) > GammaFlipSnapshotMaxAge {
			break
		}
		if (point.GammaValue > 0) != (current.GammaValue > 0) || point.GammaValue == 0 {
			break
		}
		if math.Abs(point.SpotPrice-*point.GammaFlip)/point.SpotPrice*100 <= nearPct {
			break
		}
		count++
		lastAt = point.Time
	}
	decision.Confirmed = count >= confirmations
	if decision.Confirmed {
		decision.Reason = ""
	} else {
		decision.Reason = "gamma_flip_unconfirmed"
	}
	return decision
}

func gammaFlipSnapshotUsable(snapshot GammaFlipSnapshot) bool {
	state := strings.ToLower(strings.TrimSpace(snapshot.State))
	flipState := strings.ToLower(strings.TrimSpace(snapshot.FlipState))
	return (state == "ok" || state == "partial") &&
		(flipState == "ok" || flipState == "partial") &&
		snapshot.Time.IsZero() == false &&
		snapshot.SpotPrice > 0 && !math.IsNaN(snapshot.SpotPrice) && !math.IsInf(snapshot.SpotPrice, 0) &&
		snapshot.GammaFlip != nil && *snapshot.GammaFlip > 0 && !math.IsNaN(*snapshot.GammaFlip) && !math.IsInf(*snapshot.GammaFlip, 0) &&
		!math.IsNaN(snapshot.GammaValue) && !math.IsInf(snapshot.GammaValue, 0)
}
