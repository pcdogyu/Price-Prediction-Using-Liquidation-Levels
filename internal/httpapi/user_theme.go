package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const defaultUserTheme = "dark"

func validUserTheme(theme string) bool {
	theme = strings.ToLower(strings.TrimSpace(theme))
	return theme == "dark" || theme == "light"
}

func (s *Server) userTheme(w http.ResponseWriter, r *http.Request) {
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
		theme, found, err := s.userStore.LoadUserTheme(r.Context(), username)
		if err != nil {
			problem(w, http.StatusServiceUnavailable, fmt.Errorf("load user theme: %w", err))
			return
		}
		if !found {
			theme = defaultUserTheme
		}
		if !validUserTheme(theme) {
			problem(w, http.StatusInternalServerError, errors.New("stored user theme is invalid"))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"theme": strings.ToLower(strings.TrimSpace(theme))})
	case http.MethodPut:
		var input struct {
			Theme string `json:"theme"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			problem(w, http.StatusBadRequest, fmt.Errorf("invalid theme request: %w", err))
			return
		}
		input.Theme = strings.ToLower(strings.TrimSpace(input.Theme))
		if !validUserTheme(input.Theme) {
			problem(w, http.StatusBadRequest, errors.New("theme must be dark or light"))
			return
		}
		if err := s.userStore.SaveUserTheme(r.Context(), username, input.Theme); err != nil {
			problem(w, http.StatusServiceUnavailable, fmt.Errorf("save user theme: %w", err))
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]string{"theme": input.Theme})
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		problem(w, http.StatusMethodNotAllowed, errors.New("theme endpoint only accepts GET and PUT"))
	}
}
