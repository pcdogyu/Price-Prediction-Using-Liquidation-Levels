package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

var validChartSignalPreferenceKeys = map[string]struct{}{
	"tvbot_signals": {}, "warning_signals": {}, "aggressive_signals": {},
	"steady_signals": {}, "value_area_signals": {}, "actual_trades": {},
}

func (s *Server) userChartSignalPreferences(w http.ResponseWriter, r *http.Request) {
	username, ok := s.unifiedUser(r)
	if !ok || strings.TrimSpace(username) == "" {
		problem(w, http.StatusUnauthorized, errors.New("authentication required"))
		return
	}
	if s.userStore == nil {
		problem(w, http.StatusServiceUnavailable, errors.New("user preference store is not configured"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		preferences, err := s.userStore.LoadUserChartSignalPreferences(r.Context(), username)
		if err != nil {
			problem(w, http.StatusServiceUnavailable, fmt.Errorf("load chart signal preferences: %w", err))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, preferences)
	case http.MethodPatch:
		var input struct {
			Key     string `json:"key"`
			Enabled *bool  `json:"enabled"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			problem(w, http.StatusBadRequest, fmt.Errorf("invalid chart signal preference request: %w", err))
			return
		}
		input.Key = strings.TrimSpace(input.Key)
		if _, valid := validChartSignalPreferenceKeys[input.Key]; !valid {
			problem(w, http.StatusBadRequest, errors.New("unknown chart signal preference key"))
			return
		}
		if input.Enabled == nil {
			problem(w, http.StatusBadRequest, errors.New("enabled must be a boolean"))
			return
		}
		preferences, err := s.userStore.PatchUserChartSignalPreference(r.Context(), username, input.Key, *input.Enabled)
		if err != nil {
			problem(w, http.StatusServiceUnavailable, fmt.Errorf("save chart signal preference: %w", err))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, preferences)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPatch)
		problem(w, http.StatusMethodNotAllowed, errors.New("chart signal preference endpoint only accepts GET and PATCH"))
	}
}
