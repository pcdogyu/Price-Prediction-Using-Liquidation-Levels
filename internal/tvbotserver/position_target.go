package server

import (
	"net/http"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
)

type positionTargetResponse struct {
	Unique   bool   `json:"unique"`
	Exchange string `json:"exchange,omitempty"`
	Env      string `json:"env,omitempty"`
}

func hasConfiguredOKX(store *tvbotokx.CredentialStore) bool {
	if store == nil {
		return false
	}
	for _, account := range store.Status().Credentials {
		if account.Configured {
			return true
		}
	}
	return false
}

func hasConfiguredBinance(store *tvbotbinance.CredentialStore) bool {
	if store == nil {
		return false
	}
	for _, account := range store.Status().Credentials {
		if account.Configured {
			return true
		}
	}
	return false
}

func hasConfiguredGate(store *tvbotgate.CredentialStore) bool {
	if store == nil {
		return false
	}
	for _, account := range store.Status().Credentials {
		if account.Configured {
			return true
		}
	}
	return false
}

func (s *Server) configuredPositionExchanges(env string) []string {
	var exchanges []string
	if env == tvbotconfig.EnvLive {
		if hasConfiguredOKX(s.OKXLiveCredentials) {
			exchanges = append(exchanges, "okx")
		}
		if hasConfiguredBinance(s.BinanceLiveCredentials) {
			exchanges = append(exchanges, "binance")
		}
		if hasConfiguredGate(s.GateLiveCredentials) {
			exchanges = append(exchanges, "gate")
		}
		return exchanges
	}
	if hasConfiguredOKX(s.OKXCredentials) {
		exchanges = append(exchanges, "okx")
	}
	if hasConfiguredBinance(s.BinanceCredentials) {
		exchanges = append(exchanges, "binance")
	}
	if hasConfiguredGate(s.GateCredentials) {
		exchanges = append(exchanges, "gate")
	}
	return exchanges
}

func (s *Server) positionTarget() positionTargetResponse {
	for _, env := range []string{tvbotconfig.EnvLive, tvbotconfig.EnvDemo} {
		exchanges := s.configuredPositionExchanges(env)
		if len(exchanges) == 1 {
			return positionTargetResponse{Unique: true, Exchange: exchanges[0], Env: env}
		}
		if len(exchanges) > 0 {
			return positionTargetResponse{}
		}
	}
	return positionTargetResponse{}
}

func (s *Server) handlePositionTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is allowed")
		return
	}
	writeJSON(w, http.StatusOK, s.positionTarget())
}
