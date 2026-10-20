package trading

import (
	"errors"
	"fmt"
	"math"
)

const layeredPriceLayerCount = 4

type LayeredOrderLayer struct {
	AmountPct float64 `json:"amount_pct"`
	OffsetPct float64 `json:"offset_pct"`
}

type LayeredOrderSettings struct {
	MarketAmountPct      float64             `json:"market_amount_pct"`
	SignalLimitAmountPct float64             `json:"signal_limit_amount_pct"`
	PriceLayers          []LayeredOrderLayer `json:"price_layers"`
}

type LayeredOrderLeg struct {
	Name       string
	AmountPct  float64
	AmountUSDT float64
	OrderType  OrderType
	Price      float64
	OffsetPct  float64
}

func DefaultLayeredOrderSettings() LayeredOrderSettings {
	return LayeredOrderSettings{
		MarketAmountPct:      10,
		SignalLimitAmountPct: 18,
		PriceLayers: []LayeredOrderLayer{
			{AmountPct: 18, OffsetPct: 0.5},
			{AmountPct: 18, OffsetPct: 1},
			{AmountPct: 18, OffsetPct: 2},
			{AmountPct: 18, OffsetPct: 3},
		},
	}
}

func (s LayeredOrderSettings) Normalize() LayeredOrderSettings {
	if len(s.PriceLayers) == 0 {
		return DefaultLayeredOrderSettings()
	}
	out := s
	out.PriceLayers = append([]LayeredOrderLayer(nil), s.PriceLayers...)
	return out
}

func (s LayeredOrderSettings) Validate() error {
	if math.IsNaN(s.MarketAmountPct) || math.IsInf(s.MarketAmountPct, 0) || s.MarketAmountPct < 0 {
		return errors.New("layered_order.market_amount_pct must be non-negative")
	}
	if math.IsNaN(s.SignalLimitAmountPct) || math.IsInf(s.SignalLimitAmountPct, 0) || s.SignalLimitAmountPct < 0 {
		return errors.New("layered_order.signal_limit_amount_pct must be non-negative")
	}
	if len(s.PriceLayers) != layeredPriceLayerCount {
		return fmt.Errorf("layered_order.price_layers must contain exactly %d layers", layeredPriceLayerCount)
	}
	total := s.MarketAmountPct + s.SignalLimitAmountPct
	for i, layer := range s.PriceLayers {
		if math.IsNaN(layer.AmountPct) || math.IsInf(layer.AmountPct, 0) || layer.AmountPct < 0 {
			return fmt.Errorf("layered_order.price_layers[%d].amount_pct must be non-negative", i)
		}
		if math.IsNaN(layer.OffsetPct) || math.IsInf(layer.OffsetPct, 0) || layer.OffsetPct < 0 {
			return fmt.Errorf("layered_order.price_layers[%d].offset_pct must be non-negative", i)
		}
		total += layer.AmountPct
	}
	if math.Abs(total-100) > 1e-9 {
		return fmt.Errorf("layered order amount percentages must total 100, got %s", NormalizeFloat(total))
	}
	return nil
}

func (s LayeredOrderSettings) BuildPlan(action Side, amountUSDT, signalPrice, realtimePrice float64) ([]LayeredOrderLeg, error) {
	s = s.Normalize()
	if err := s.Validate(); err != nil {
		return nil, err
	}
	if amountUSDT <= 0 || signalPrice <= 0 || realtimePrice <= 0 {
		return nil, errors.New("layered order amount and prices must be positive")
	}
	legs := []LayeredOrderLeg{
		{Name: "market", AmountPct: s.MarketAmountPct, AmountUSDT: amountUSDT * s.MarketAmountPct / 100, OrderType: OrderTypeMarket, Price: realtimePrice},
		{Name: "signal_limit", AmountPct: s.SignalLimitAmountPct, AmountUSDT: amountUSDT * s.SignalLimitAmountPct / 100, OrderType: OrderTypeLimit, Price: signalPrice},
	}
	for i, layer := range s.PriceLayers {
		price := realtimePrice * (1 - layer.OffsetPct/100)
		if action == ActionShort {
			price = realtimePrice * (1 + layer.OffsetPct/100)
		}
		legs = append(legs, LayeredOrderLeg{
			Name:       fmt.Sprintf("price_%d", i+1),
			AmountPct:  layer.AmountPct,
			AmountUSDT: amountUSDT * layer.AmountPct / 100,
			OrderType:  OrderTypeLimit,
			Price:      price,
			OffsetPct:  layer.OffsetPct,
		})
	}
	return legs, nil
}
