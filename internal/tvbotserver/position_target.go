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
	primary := s.primaryExchangeForConfig(s.ConfigStore.Get())
	if primary == "" || !s.exchangeConfiguredInEnvironment(primary, env) {
		return nil
	}
	return []string{primary}
}

func (s *Server) positionTarget() positionTargetResponse {
	primary := s.primaryExchangeForConfig(s.ConfigStore.Get())
	if primary == "" {
		return positionTargetResponse{}
	}
	for _, env := range []string{tvbotconfig.EnvLive, tvbotconfig.EnvDemo} {
		if s.exchangeConfiguredInEnvironment(primary, env) {
			return positionTargetResponse{Unique: true, Exchange: primary, Env: env}
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
