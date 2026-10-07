package volumeprofile

import (
	"errors"
	"math"
	"sort"
	"time"
	_ "time/tzdata"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

const (
	Rows              = 100
	ValueAreaFraction = .70
)

type Session struct {
	Start     time.Time
	End       time.Time
	ResetKind string
}

type Level struct {
	Price     float64
	VolumeUSD float64
}

type boundary struct {
	at   time.Time
	kind string
}

// SessionAt returns the interval bounded by 08:00 Asia/Shanghai and 09:30
// America/New_York. Both boundaries are applied every calendar day; the IANA
// timezone database supplies the US daylight-saving transition automatically.
func SessionAt(now time.Time) (Session, error) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return Session{}, err
	}
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		return Session{}, err
	}
	now = now.UTC()
	local := now.In(shanghai)
	var candidates []boundary
	for offset := -2; offset <= 2; offset++ {
		date := local.AddDate(0, 0, offset)
		y, m, d := date.Date()
		candidates = append(candidates,
			boundary{time.Date(y, m, d, 8, 0, 0, 0, shanghai).UTC(), "shanghai_0800"},
			boundary{time.Date(y, m, d, 9, 30, 0, 0, newYork).UTC(), "new_york_0930"},
		)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].at.Before(candidates[j].at) })
	start := -1
	for i := range candidates {
		if !candidates[i].at.After(now) {
			start = i
		}
	}
	if start < 0 || start+1 >= len(candidates) {
		return Session{}, errors.New("could not resolve volume profile session")
	}
	return Session{Start: candidates[start].at, End: candidates[start+1].at, ResetKind: candidates[start].kind}, nil
}

func Build(symbol string, session Session, levels []Level, dataThrough, now time.Time, complete bool) domain.VolumeProfile {
	now, dataThrough = now.UTC(), dataThrough.UTC()
	profile := domain.VolumeProfile{
		Symbol: symbol, Source: domain.DataSourceBinanceUSDM,
		SessionStart: session.Start.UTC(), SessionEnd: session.End.UTC(), NextReset: session.End.UTC(),
		ResetKind: session.ResetKind, ValueAreaFraction: ValueAreaFraction,
		Bins: []domain.VolumeProfileBin{}, DataThrough: dataThrough, UpdatedAt: now,
	}
	if now.After(session.Start) && dataThrough.After(session.Start) {
		denominator := now.Sub(session.Start)
		if denominator > 0 {
			profile.BackfillProgress = clamp(dataThrough.Sub(session.Start).Seconds()/denominator.Seconds(), 0, 1)
		}
	}
	if complete {
		profile.BackfillProgress = 1
	}
	valid := make([]Level, 0, len(levels))
	for _, level := range levels {
		if level.Price > 0 && level.VolumeUSD > 0 {
			valid = append(valid, level)
			profile.TotalVolumeUSD += level.VolumeUSD
		}
	}
	if len(valid) == 0 || profile.TotalVolumeUSD <= 0 {
		profile.State = "unavailable"
		if !complete {
			profile.State = "backfilling"
		}
		return profile
	}
	minPrice, maxPrice := valid[0].Price, valid[0].Price
	for _, level := range valid[1:] {
		minPrice = math.Min(minPrice, level.Price)
		maxPrice = math.Max(maxPrice, level.Price)
	}
	count := Rows
	width := (maxPrice - minPrice) / float64(count)
	if width <= 0 {
		count = 1
		width = math.Max(minPrice*1e-8, 1e-8)
		minPrice -= width / 2
		maxPrice = minPrice + width
	}
	profile.Bins = make([]domain.VolumeProfileBin, count)
	for i := range profile.Bins {
		profile.Bins[i].PriceLow = minPrice + float64(i)*width
		profile.Bins[i].PriceHigh = minPrice + float64(i+1)*width
	}
	for _, level := range valid {
		index := int((level.Price - minPrice) / width)
		if index < 0 {
			index = 0
		}
		if index >= count {
			index = count - 1
		}
		profile.Bins[index].VolumeUSD += level.VolumeUSD
	}
	for i := range profile.Bins {
		profile.Bins[i].VolumeShare = profile.Bins[i].VolumeUSD / profile.TotalVolumeUSD * 100
	}
	if !complete {
		profile.State = "backfilling"
		return profile
	}
	profile.State = "ok"
	if dataThrough.IsZero() || now.Sub(dataThrough) > 30*time.Second {
		profile.State = "stale"
	}
	low, high := valueArea(profile.Bins, profile.TotalVolumeUSD*ValueAreaFraction)
	for i := low; i <= high; i++ {
		profile.Bins[i].InValueArea = true
	}
	val, vah := profile.Bins[low].PriceLow, profile.Bins[high].PriceHigh
	profile.VAL, profile.VAH = &val, &vah
	return profile
}

func valueArea(bins []domain.VolumeProfileBin, target float64) (int, int) {
	poc := 0
	for i := 1; i < len(bins); i++ {
		if bins[i].VolumeUSD > bins[poc].VolumeUSD {
			poc = i
		}
	}
	low, high, accumulated := poc, poc, bins[poc].VolumeUSD
	for accumulated < target && (low > 0 || high+1 < len(bins)) {
		lower, upper := -1.0, -1.0
		if low > 0 {
			lower = bins[low-1].VolumeUSD
		}
		if high+1 < len(bins) {
			upper = bins[high+1].VolumeUSD
		}
		switch {
		case lower == upper && lower >= 0:
			low--
			accumulated += lower
			// Equal adjacent rows are one expansion step. Include both even if
			// the first row alone crosses the target so the area stays unbiased.
			if high+1 < len(bins) {
				high++
				accumulated += upper
			}
		case lower > upper:
			low--
			accumulated += lower
		default:
			high++
			accumulated += upper
		}
	}
	return low, high
}

func clamp(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
