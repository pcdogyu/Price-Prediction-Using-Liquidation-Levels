package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

type positionOpenModeRequest struct {
	Exchange string `json:"exchange"`
	APIID    string `json:"api_id"`
	InstID   string `json:"inst_id"`
	PosSide  string `json:"pos_side"`
	Mode     string `json:"mode"`
}

type positionOpenModeResponse struct {
	OK      bool   `json:"ok"`
	Key     string `json:"key"`
	Mode    string `json:"mode"`
	Message string `json:"message,omitempty"`
}

func normalizePositionOpenMode(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" {
		mode = string(tvbottrading.OrderTypeMarket)
	}
	switch mode {
	case string(tvbottrading.OrderTypeMarket), string(tvbottrading.OrderTypeLimit):
		return mode, nil
	default:
		return "", errors.New("mode must be market or limit")
	}
}

func positionOpenModeKey(exchange, apiID, instID, posSide string) string {
	posSide = normalizePosSide(posSide)
	if posSide == "" {
		posSide = "net"
	}
	return strings.Join([]string{
		tvbottrading.NormalizeExchange(exchange),
		strings.TrimSpace(apiID),
		strings.ToUpper(strings.TrimSpace(instID)),
		posSide,
	}, "|")
}

func (s *Server) handlePositionOpenMode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only PUT is allowed")
		return
	}
	var req positionOpenModeRequest
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.Exchange = tvbottrading.NormalizeExchange(req.Exchange)
	req.APIID = strings.TrimSpace(req.APIID)
	req.InstID = strings.ToUpper(strings.TrimSpace(req.InstID))
	req.PosSide = normalizePosSide(req.PosSide)
	mode, err := normalizePositionOpenMode(req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_position_open_mode", err.Error())
		return
	}
	if req.InstID == "" {
		writeError(w, http.StatusBadRequest, "invalid_position_open_mode", "inst_id is required")
		return
	}
	if req.Exchange != tvbottrading.ExchangeOKX && req.Exchange != tvbottrading.ExchangeBinance && req.Exchange != tvbottrading.ExchangeGate {
		writeError(w, http.StatusBadRequest, "invalid_position_open_mode", "unsupported exchange")
		return
	}
	key := positionOpenModeKey(req.Exchange, req.APIID, req.InstID, req.PosSide)
	if _, err := s.ConfigStore.UpdateSettings(func(cfg *tvbotconfig.Config) error {
		if cfg.UI.PositionOpenModes == nil {
			cfg.UI.PositionOpenModes = make(map[string]string)
		}
		cfg.UI.PositionOpenModes[key] = mode
		return nil
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "position_open_mode_save_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, positionOpenModeResponse{
		OK:      true,
		Key:     key,
		Mode:    mode,
		Message: "position open mode saved",
	})
}
