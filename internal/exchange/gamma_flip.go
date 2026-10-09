package exchange

import (
	"math"
	"sort"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

const gammaFlipMethod = "Gamma Flip：固定当前 markIV、持仓量、利率与到期时间，按 Black–Scholes 重估不同标的价格下的净 GEX（CALL 正、PUT 负）；在现价 50%～150% 内寻找符号翻转，多处交叉时显示最近现价的一处。缺失 IV 或持仓数据时标记部分估算。"

type flipContract struct {
	center, width, logWeight, sign float64
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Gamma(S) * OI * unit * S² has a positive factor common to the chain.
// Removing that factor preserves its zero crossings. Log normalization keeps
// far-out-of-the-money contracts from underflowing into a spurious zero.
func flipRatio(logSpot float64, contracts []flipContract) float64 {
	maximum := math.Inf(-1)
	for _, c := range contracts {
		d := (logSpot - c.center) / c.width
		maximum = math.Max(maximum, c.logWeight-.5*d*d)
	}
	if math.IsInf(maximum, -1) {
		return 0
	}
	net, absolute := 0., 0.
	for _, c := range contracts {
		d := (logSpot - c.center) / c.width
		weight := math.Exp(c.logWeight - .5*d*d - maximum)
		net += c.sign * weight
		absolute += weight
	}
	return net / absolute
}

func flipSign(value float64) int {
	if math.Abs(value) <= 1e-10 {
		return 0
	}
	if value > 0 {
		return 1
	}
	return -1
}

func flipRoot(low, high float64, contracts []flipContract) float64 {
	sign := flipSign(flipRatio(low, contracts))
	for i := 0; i < 60; i++ {
		mid := (low + high) / 2
		value := flipRatio(mid, contracts)
		if math.Abs(value) < 1e-12 || high-low < 1e-10 {
			return math.Exp(mid)
		}
		if flipSign(value) == sign {
			low = mid
		} else {
			high = mid
		}
	}
	return math.Exp((low + high) / 2)
}

func buildGammaFlip(g *domain.GammaView, rows []OptionExposure, at time.Time) {
	g.FlipState = "unavailable"
	g.FlipMethod = gammaFlipMethod
	g.GammaFlips = []float64{}
	if !finite(g.SpotPrice) || g.SpotPrice <= 0 {
		return
	}
	g.FlipRangeLow, g.FlipRangeHigh = g.SpotPrice*.5, g.SpotPrice*1.5
	low, high := math.Log(g.FlipRangeLow), math.Log(g.FlipRangeHigh)
	contracts := []flipContract{}
	for _, row := range rows {
		strike := f(row.Contract.Strike)
		if !finite(strike) || strike <= 0 || !finite(row.Contract.Unit) || row.Contract.Unit <= 0 || !finite(row.OI) || row.OI <= 0 || !finite(row.Gamma) || row.Gamma < 0 || (row.Contract.Side != "CALL" && row.Contract.Side != "PUT") || row.Contract.Expiry <= at.UnixMilli() {
			continue
		}
		g.FlipExpectedContracts++
		if row.IV == nil || !finite(*row.IV) || *row.IV <= 0 || row.InterestRate == nil || !finite(*row.InterestRate) {
			continue
		}
		years := time.UnixMilli(row.Contract.Expiry).Sub(at).Seconds() / (365 * 24 * 60 * 60)
		iv := *row.IV
		width := iv * math.Sqrt(years)
		center := math.Log(strike) - (*row.InterestRate+.5*iv*iv)*years
		logWeight := math.Log(row.OI) + math.Log(row.Contract.Unit) - math.Log(width)
		if !finite(width) || width <= 0 || !finite(center) || !finite(logWeight) {
			continue
		}
		sign := 1.
		if row.Contract.Side == "PUT" {
			sign = -1
		}
		contracts = append(contracts, flipContract{center: center, width: width, logWeight: logWeight, sign: sign})
	}
	g.FlipContracts = len(contracts)
	if len(contracts) == 0 {
		return
	}
	grid := make([]float64, 0, 401+21*len(contracts))
	for i := 0; i <= 400; i++ {
		grid = append(grid, low+(high-low)*float64(i)/400)
	}
	grid = append(grid, math.Log(g.SpotPrice))
	// Resolve narrow near-expiry Gamma peaks as well as the broad price grid.
	for _, c := range contracts {
		for i := -10; i <= 10; i++ {
			value := c.center + float64(i)*.5*c.width
			if value > low && value < high {
				grid = append(grid, value)
			}
		}
	}
	sort.Float64s(grid)
	previous, previousSign := 0., 0
	for _, value := range grid {
		sign := flipSign(flipRatio(value, contracts))
		if sign == 0 {
			continue
		} // A zero alone is not a sign reversal.
		if previousSign != 0 && sign != previousSign {
			root := flipRoot(previous, value, contracts)
			if len(g.GammaFlips) == 0 || math.Abs(root-g.GammaFlips[len(g.GammaFlips)-1]) > g.SpotPrice*1e-7 {
				g.GammaFlips = append(g.GammaFlips, root)
			}
		}
		previous, previousSign = value, sign
	}
	if len(g.GammaFlips) == 0 {
		g.FlipState = "no_crossing"
		return
	}
	closest := g.GammaFlips[0]
	for _, value := range g.GammaFlips[1:] {
		if math.Abs(value-g.SpotPrice) < math.Abs(closest-g.SpotPrice) {
			closest = value
		}
	}
	g.GammaFlip = &closest
	g.FlipState = "ok"
	if g.FlipContracts != g.FlipExpectedContracts {
		g.FlipState = "partial"
	}
}
