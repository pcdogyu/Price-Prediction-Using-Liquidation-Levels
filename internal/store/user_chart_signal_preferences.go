package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// UserChartSignalPreferences controls which historical signal and trade marker
// groups are visible on a user's liquidation chart.
type UserChartSignalPreferences struct {
	TVBotSignals      bool  `json:"tvbot_signals"`
	WarningSignals    bool  `json:"warning_signals"`
	AggressiveSignals bool  `json:"aggressive_signals"`
	SteadySignals     bool  `json:"steady_signals"`
	ValueAreaSignals  bool  `json:"value_area_signals"`
	ActualTrades      bool  `json:"actual_trades"`
	UpCandles         bool  `json:"up_candles"`
	DownCandles       bool  `json:"down_candles"`
	LongLiquidations  bool  `json:"long_liquidations"`
	ShortLiquidations bool  `json:"short_liquidations"`
	VolumeProfile     bool  `json:"volume_profile"`
	TopVolumeBins     bool  `json:"top_volume_bins"`
	Leverage10x       bool  `json:"leverage_10x"`
	Leverage25x       bool  `json:"leverage_25x"`
	Leverage50x       bool  `json:"leverage_50x"`
	Leverage100x      bool  `json:"leverage_100x"`
	ValLine           bool  `json:"val_line"`
	VahLine           bool  `json:"vah_line"`
	ModelSignals      bool  `json:"model_signals"`
	UpdatedAt         int64 `json:"updated_at"`
}

func defaultUserChartSignalPreferences() UserChartSignalPreferences {
	return UserChartSignalPreferences{
		TVBotSignals: true, WarningSignals: true, AggressiveSignals: true,
		SteadySignals: true, ValueAreaSignals: true, ActualTrades: true,
		UpCandles: true, DownCandles: true, LongLiquidations: true, ShortLiquidations: true,
		VolumeProfile: true, TopVolumeBins: true,
		Leverage10x: true, Leverage25x: true, Leverage50x: true, Leverage100x: true,
		ValLine: true, VahLine: true, ModelSignals: true,
	}
}

// LoadUserChartSignalPreferences returns defaults for users without a saved
// row. UpdatedAt is monotonic per user and is used by open chart pages to poll
// for changes made on other devices.
func (s *Store) LoadUserChartSignalPreferences(ctx context.Context, username string) (UserChartSignalPreferences, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return UserChartSignalPreferences{}, errors.New("chart signal preference username is empty")
	}
	preferences := defaultUserChartSignalPreferences()
	var tvbot, warning, aggressive, steady, valueArea, trades int
	var upCandles, downCandles, longLiquidations, shortLiquidations, volumeProfile, topVolumeBins int
	var leverage10x, leverage25x, leverage50x, leverage100x, valLine, vahLine, modelSignals int
	err := s.settings().QueryRowContext(ctx, `SELECT tvbot_signals,warning_signals,aggressive_signals,steady_signals,value_area_signals,actual_trades,up_candles,down_candles,long_liquidations,short_liquidations,volume_profile,top_volume_bins,leverage_10x,leverage_25x,leverage_50x,leverage_100x,val_line,vah_line,model_signals,updated_ts FROM user_chart_signal_preferences WHERE username=?`, username).Scan(
		&tvbot, &warning, &aggressive, &steady, &valueArea, &trades,
		&upCandles, &downCandles, &longLiquidations, &shortLiquidations, &volumeProfile, &topVolumeBins,
		&leverage10x, &leverage25x, &leverage50x, &leverage100x, &valLine, &vahLine, &modelSignals,
		&preferences.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return preferences, nil
	}
	if err != nil {
		return UserChartSignalPreferences{}, err
	}
	preferences.TVBotSignals = tvbot != 0
	preferences.WarningSignals = warning != 0
	preferences.AggressiveSignals = aggressive != 0
	preferences.SteadySignals = steady != 0
	preferences.ValueAreaSignals = valueArea != 0
	preferences.ActualTrades = trades != 0
	preferences.UpCandles = upCandles != 0
	preferences.DownCandles = downCandles != 0
	preferences.LongLiquidations = longLiquidations != 0
	preferences.ShortLiquidations = shortLiquidations != 0
	preferences.VolumeProfile = volumeProfile != 0
	preferences.TopVolumeBins = topVolumeBins != 0
	preferences.Leverage10x = leverage10x != 0
	preferences.Leverage25x = leverage25x != 0
	preferences.Leverage50x = leverage50x != 0
	preferences.Leverage100x = leverage100x != 0
	preferences.ValLine = valLine != 0
	preferences.VahLine = vahLine != 0
	preferences.ModelSignals = modelSignals != 0
	return preferences, nil
}

// PatchUserChartSignalPreference atomically updates only the named group so
// concurrent changes to different groups on separate devices do not overwrite
// one another.
func (s *Store) PatchUserChartSignalPreference(ctx context.Context, username, key string, enabled bool) (UserChartSignalPreferences, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return UserChartSignalPreferences{}, errors.New("chart signal preference username is empty")
	}
	column := ""
	switch key {
	case "tvbot_signals":
		column = "tvbot_signals"
	case "warning_signals":
		column = "warning_signals"
	case "aggressive_signals":
		column = "aggressive_signals"
	case "steady_signals":
		column = "steady_signals"
	case "value_area_signals":
		column = "value_area_signals"
	case "actual_trades":
		column = "actual_trades"
	case "up_candles":
		column = "up_candles"
	case "down_candles":
		column = "down_candles"
	case "long_liquidations":
		column = "long_liquidations"
	case "short_liquidations":
		column = "short_liquidations"
	case "volume_profile":
		column = "volume_profile"
	case "top_volume_bins":
		column = "top_volume_bins"
	case "leverage_10x":
		column = "leverage_10x"
	case "leverage_25x":
		column = "leverage_25x"
	case "leverage_50x":
		column = "leverage_50x"
	case "leverage_100x":
		column = "leverage_100x"
	case "val_line":
		column = "val_line"
	case "vah_line":
		column = "vah_line"
	case "model_signals":
		column = "model_signals"
	default:
		return UserChartSignalPreferences{}, errors.New("unknown chart signal preference")
	}
	value := 0
	if enabled {
		value = 1
	}
	updatedAt := time.Now().UTC().UnixMilli()
	query := `INSERT INTO user_chart_signal_preferences(username,` + column + `,updated_ts) VALUES(?,?,?) ` +
		`ON CONFLICT(username) DO UPDATE SET ` + column + `=excluded.` + column + `,updated_ts=MAX(user_chart_signal_preferences.updated_ts+1,excluded.updated_ts)`
	if _, err := s.settings().ExecContext(ctx, query, username, value, updatedAt); err != nil {
		return UserChartSignalPreferences{}, err
	}
	return s.LoadUserChartSignalPreferences(ctx, username)
}
