package trading

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

func SizeFromUSDTNotional(amountUSDT, price, ctVal, lotSz, minSz float64) (string, error) {
	if amountUSDT <= 0 {
		return "", fmt.Errorf("amount must be positive")
	}
	if price <= 0 {
		return "", fmt.Errorf("price must be positive")
	}
	if ctVal <= 0 || lotSz <= 0 || minSz <= 0 {
		return "", fmt.Errorf("contract metadata must be positive")
	}
	raw := amountUSDT / (ctVal * price)
	return SizeFromContracts(raw, lotSz, minSz)
}

func SizeFromContracts(raw, lotSz, minSz float64) (string, error) {
	if raw <= 0 {
		return "", fmt.Errorf("contract size must be positive")
	}
	if lotSz <= 0 || minSz <= 0 {
		return "", fmt.Errorf("contract metadata must be positive")
	}
	steps := math.Floor(raw/lotSz + 1e-12)
	size := steps * lotSz
	if size+1e-12 < minSz {
		return "", fmt.Errorf("calculated contract size %s is below min size %s", NormalizeFloat(size), NormalizeFloat(minSz))
	}
	return formatStepDecimal(size, lotSz), nil
}

func formatStepDecimal(v, step float64) string {
	decimals := decimalsForStep(step)
	s := strconv.FormatFloat(v, 'f', decimals, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	if s == "" {
		return "0"
	}
	return s
}

func decimalsForStep(step float64) int {
	s := strconv.FormatFloat(step, 'f', 12, 64)
	s = strings.TrimRight(s, "0")
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return len(s) - i - 1
	}
	return 0
}
