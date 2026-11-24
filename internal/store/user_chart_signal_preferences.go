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
	UpdatedAt         int64 `json:"updated_at"`
}

func defaultUserChartSignalPreferences() UserChartSignalPreferences {
	return UserChartSignalPreferences{
		TVBotSignals: true, WarningSignals: true, AggressiveSignals: true,
		SteadySignals: true, ValueAreaSignals: true, ActualTrades: true,
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
	err := s.settings().QueryRowContext(ctx, `SELECT tvbot_signals,warning_signals,aggressive_signals,steady_signals,value_area_signals,actual_trades,updated_ts FROM user_chart_signal_preferences WHERE username=?`, username).Scan(&tvbot, &warning, &aggressive, &steady, &valueArea, &trades, &preferences.UpdatedAt)
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
