package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotsecurity"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotupgrade"
)

type Server struct {
	ConfigStore            *config.Store
	Orders                 *storage.OrderStore
	Token                  security.TokenService
	Executor               trading.Executor
	OKXCredentials         *okx.CredentialStore
	OKXLiveCredentials     *okx.CredentialStore
	BinanceCredentials     *binance.CredentialStore
	BinanceLiveCredentials *binance.CredentialStore
	GateCredentials        *gate.CredentialStore
	GateLiveCredentials    *gate.CredentialStore
	OKXHTTPClient          *http.Client
	BinanceHTTPClient      *http.Client
	GateHTTPClient         *http.Client
	AdminToken             string
	AdminUser              string
	AdminPass              string
	Logger                 *slog.Logger
	Now                    func() time.Time
	Upgrade                *upgrade.Manager
	BuildInfo              BuildInfo
	RequireUnifiedUser     func(http.ResponseWriter, *http.Request) bool
	RequireUnifiedAdmin    func(http.ResponseWriter, *http.Request) bool
	CurrentUser            func(*http.Request) (string, bool)
	UserCredentialResolver UserCredentialResolver
	TenantID               string
	TenantRouter           TenantRouter
	RedirectUnifiedLogin   func(http.ResponseWriter, *http.Request)
	LogoutUnified          func(http.ResponseWriter, *http.Request)
	TopMenuProvider        func(*http.Request) []TopMenuItem
	positionEntryCache     *positionEntryFillCache
}

type UserCredentials struct {
	OKX         *okx.CredentialStore
	OKXLive     *okx.CredentialStore
	Binance     *binance.CredentialStore
	BinanceLive *binance.CredentialStore
	Gate        *gate.CredentialStore
	GateLive    *gate.CredentialStore
}

type UserCredentialResolver interface {
	ForUser(username string) (UserCredentials, error)
}

// UserScopeResolver optionally resolves every user-owned TVBot resource in one
// operation. The older credential-only resolver remains supported for tests and
// deployments that have not enabled tenant storage yet.
type UserScopeResolver interface {
	ForUserScope(username string) (UserScope, error)
}

type UserScope struct {
	TenantID    string
	ConfigStore *config.Store
	Orders      *storage.OrderStore
	Credentials UserCredentials
}

// TenantRouter is used by the public TradingView webhook. A tenant id is
// carried in generated templates, so one process can route the webhook to the
// same config and order database that served the user's dashboard.
type TenantRouter interface {
	ForTenantID(tenantID string) (UserScope, error)
	DefaultTenantID() string
}

func (s *Server) SetUnifiedAuth(user, admin func(http.ResponseWriter, *http.Request) bool, currentUser func(*http.Request) (string, bool), redirect func(http.ResponseWriter, *http.Request), logout func(http.ResponseWriter, *http.Request)) {
	s.RequireUnifiedUser = user
	s.RequireUnifiedAdmin = admin
	s.CurrentUser = currentUser
	s.RedirectUnifiedLogin = redirect
	s.LogoutUnified = logout
}

func (s *Server) SetTopMenuProvider(provider func(*http.Request) []TopMenuItem) {
	s.TopMenuProvider = provider
}

const (
	adminSessionCookieName    = "tvbot_admin_session"
	adminSessionTTL           = 12 * time.Hour
	symbolCatalogSyncInterval = 24 * time.Hour
)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/":
		http.Redirect(w, r, "https://www.mext.go.jp/", http.StatusFound)
	case r.URL.Path == "/tvorder":
		s.handleTVOrder(w, r)
	case r.URL.Path == "/tvbot" || strings.HasPrefix(r.URL.Path, "/tvbot/"):
		s.handleTVBot(w, r)
	case r.URL.Path == "/upgrade" || r.URL.Path == "/upgrade/":
		s.handleUpgrade(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "valid api roots are /tvorder, /tvbot and /upgrade")
	}
}

func (s *Server) handleTVOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "/tvorder only accepts POST")
		return
	}
	if s.TenantRouter != nil && strings.TrimSpace(s.TenantID) == "" {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
		tenantID := s.TenantRouter.DefaultTenantID()
		var envelope struct {
			TenantID string `json:"tenant_id"`
		}
		if json.Unmarshal(body, &envelope) == nil && strings.TrimSpace(envelope.TenantID) != "" {
			tenantID = strings.TrimSpace(envelope.TenantID)
		}
		scope, err := s.TenantRouter.ForTenantID(tenantID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_tenant", err.Error())
			return
		}
		scoped := s.withUserScope(scope)
		scoped.TenantRouter = nil
		replay := r.Clone(r.Context())
		replay.Body = io.NopCloser(bytes.NewReader(body))
		scoped.handleTVOrder(w, replay)
		return
	}
	now := s.now()
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		s.recordTVOrderRejected(r, trading.Signal{}, "bad_json", err, now)
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	rawJSON := sanitizedRawWebhookJSON(body)
	var signal trading.Signal
	if err := json.Unmarshal(body, &signal); err != nil {
		preview := signalPreviewFromJSON(body)
		preview.RawJSON = rawJSON
		s.recordTVOrderRejected(r, preview, "bad_json", err, now)
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	signal.RawJSON = rawJSON
	targetExchangeProvided := strings.TrimSpace(signal.TargetExchange) != ""
	tradeEnvProvided := jsonFieldProvided(body, "trade_env")
	signal.Normalize()
	tokenSignal := signal
	if !tradeEnvProvided {
		signal.TradeEnv = trading.TradeEnvDemo
	}
	applySignalSourceExchangeRouting(&signal, targetExchangeProvided)
	cfg := configForTradeEnv(s.ConfigStore.Get(), signal.TradeEnv)
	cfg.OrderSettings().ApplyToSignal(&signal)
	if err := signal.Validate(now, time.Duration(cfg.Trading.SignalTTLSeconds)*time.Second, cfg); err != nil {
		s.recordTVOrderRejected(r, signal, "invalid_signal", err, now)
		writeError(w, http.StatusBadRequest, "invalid_signal", err.Error())
		return
	}
	if !s.validSignalToken(tokenSignal, signal, tradeEnvProvided) {
		s.recordTVOrderRejected(r, signal, "invalid_token", errors.New("token validation failed"), now)
		writeError(w, http.StatusUnauthorized, "invalid_token", "token validation failed")
		return
	}
	signal.SourceAction = signal.Action
	classErr := applyTVOrderPositionSemantics(&signal)
	if classErr == nil {
		adxDecision, adxErr := applyTVOrderADXRouting(&signal)
		if adxErr != nil {
			s.recordTVOrderRejected(r, signal, "invalid_adx_metadata", adxErr, now)
			writeError(w, http.StatusBadRequest, "invalid_adx_metadata", adxErr.Error())
			return
		}
		if adxDecision.IgnoreEntry {
			record, err := s.Orders.RecordIgnoredReason(signal, "adx_transition", "ADX is in the 20 to 25 transition range", now)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "store_error", err.Error())
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{
				"status":    "ignored",
				"signal_id": record.SignalID,
				"reason":    "adx_transition",
			})
			return
		}
		if block, blocked, err := s.activeCoinpairCooldown(signal, now); err != nil {
			writeError(w, http.StatusServiceUnavailable, "cooldown_check_failed", err.Error())
			return
		} else if blocked {
			record, err := s.recordCooldownIgnoredSignal(signal, block, now)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "store_error", err.Error())
				return
			}
			writeJSON(w, http.StatusAccepted, coinpairCooldownResponse(record, block))
			return
		}
		if filter, ignored := ignoredEntrySignal(cfg, signal); ignored {
			record, err := s.Orders.RecordIgnored(signal, filter, now)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "store_error", err.Error())
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{
				"status":    "ignored",
				"signal_id": record.SignalID,
			})
			return
		}
		if filter, matched := coinpairWhitelistMatch(cfg, signal); !matched {
			record, err := s.Orders.RecordIgnoredReason(signal, "coinpair_whitelist", fmt.Sprintf("coinpair did not match coinpair whitelist %q", filter), now)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "store_error", err.Error())
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{
				"status":    "ignored",
				"signal_id": record.SignalID,
				"reason":    "coinpair_whitelist",
			})
			return
		}
		if signal.PositionEffect != trading.PositionEffectClose {
			decision, err := s.marketScopeDecision(signal)
			if err != nil {
				code := marketScopeCheckFailedCode(decision)
				s.recordTVOrderRejected(r, signal, code, err, now)
				writeError(w, http.StatusServiceUnavailable, code, err.Error())
				return
			}
			if !decision.Available {
				err := errors.New(marketScopeUnavailableMessage(decision))
				code := marketScopeUnavailableCode(decision)
				s.recordTVOrderRejected(r, signal, code, err, now)
				writeError(w, http.StatusServiceUnavailable, code, err.Error())
				return
			}
			if !decision.Allowed {
				record, err := s.recordMarketScopeIgnoredSignal(signal, decision, now)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "store_error", err.Error())
					return
				}
				writeJSON(w, http.StatusAccepted, marketScopeIgnoredResponse(record, decision))
				return
			}
		}
	}
	dedupeKey := storage.DedupeKey(signal)
	record, duplicate, err := s.Orders.RecordAccepted(signal, dedupeKey, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if duplicate {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":    "duplicate",
			"signal_id": record.SignalID,
		})
		return
	}
	if classErr != nil {
		_ = s.Orders.MarkFailedCode(record.SignalID, "invalid_position_intent", classErr, now)
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":    "accepted",
			"signal_id": record.SignalID,
		})
		return
	}
	if signal.PositionEffect == trading.PositionEffectClose {
		go s.executePositionCloseSignal(record.SignalID, signal, cfg)
	} else {
		go s.execute(record.SignalID, signal, cfg)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "accepted",
		"signal_id": record.SignalID,
	})
}

func (s *Server) recordTVOrderRejected(r *http.Request, signal trading.Signal, code string, err error, now time.Time) {
	s.logTVOrderRejected(r, signal, code, err)
	if s.Orders == nil {
		return
	}
	if _, storeErr := s.Orders.RecordRejected(signal, code, err, now); storeErr != nil && s.Logger != nil {
		s.Logger.Error("failed to record rejected tvorder", "code", code, "error", storeErr)
	}
}

func sanitizedRawWebhookJSON(body []byte) string {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	b, err := json.MarshalIndent(redactSensitiveJSONFields(payload), "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}

func redactSensitiveJSONFields(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "token", "token_nonce":
				out[key] = "[redacted]"
			default:
				out[key] = redactSensitiveJSONFields(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = redactSensitiveJSONFields(item)
		}
		return out
	default:
		return typed
	}
}

func jsonFieldProvided(body []byte, field string) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return false
	}
	_, ok := fields[field]
	return ok
}

func (s *Server) validSignalToken(raw, applied trading.Signal, tradeEnvProvided bool) bool {
	payloads := []string{
		applied.CanonicalNonceWebhookTokenPayload(),
		raw.CanonicalNonceWebhookTokenPayload(),
		applied.CanonicalWebhookTokenPayload(),
		raw.CanonicalWebhookTokenPayload(),
	}
	// The liquidation alert service historically used one operator-managed
	// webhook token without an embedded tenant. Keep accepting that token for
	// tenant-routed alert payloads while tenant-specific template tokens use the
	// stronger v8 payload above. Explicit trade_env requests without a tenant
	// still retain the existing legacy-token rejection behavior.
	if !tradeEnvProvided || strings.TrimSpace(applied.TenantID) != "" || strings.TrimSpace(raw.TenantID) != "" {
		payloads = append(payloads,
			trading.CanonicalTargetTradeEnvWebhookTokenPayloadWithNonce(applied.TargetExchange, applied.APIID, applied.TradeEnv, applied.TokenNonce),
			trading.CanonicalTargetTradeEnvWebhookTokenPayloadWithNonce(raw.TargetExchange, raw.APIID, raw.TradeEnv, raw.TokenNonce),
			trading.CanonicalTargetTradeEnvWebhookTokenPayload(applied.TargetExchange, applied.APIID, applied.TradeEnv),
			trading.CanonicalTargetTradeEnvWebhookTokenPayload(raw.TargetExchange, raw.APIID, raw.TradeEnv),
			trading.CanonicalTargetWebhookTokenPayloadWithNonce(applied.TargetExchange, applied.APIID, applied.TokenNonce),
			trading.CanonicalTargetWebhookTokenPayloadWithNonce(raw.TargetExchange, raw.APIID, raw.TokenNonce),
			trading.CanonicalTargetWebhookTokenPayload(applied.TargetExchange, applied.APIID),
			trading.CanonicalTargetWebhookTokenPayload(raw.TargetExchange, raw.APIID),
			applied.CanonicalTokenPayload(),
			raw.CanonicalTokenPayload(),
		)
	}
	seen := map[string]bool{}
	for _, payload := range payloads {
		if seen[payload] {
			continue
		}
		seen[payload] = true
		if s.Token.Validate(payload, applied.Token) {
			return true
		}
	}
	return false
}

func configForTradeEnv(cfg config.Config, tradeEnv string) config.Config {
	switch trading.NormalizeTradeEnv(tradeEnv) {
	case trading.TradeEnvLive:
		cfg.Trading.Env = config.EnvLive
	default:
		cfg.Trading.Env = config.EnvDemo
	}
	return cfg
}

func ignoredEntrySignal(cfg config.Config, signal trading.Signal) (string, bool) {
	if strings.EqualFold(strings.TrimSpace(signal.PositionEffect), trading.PositionEffectClose) {
		return "", false
	}
	for _, keyword := range cfg.Trading.IgnoredCoinpairs {
		filter := normalizeCoinpairFilter(keyword)
		if filter == "" {
			continue
		}
		for _, candidate := range []string{signal.Coinpair, signal.Ticker} {
			if strings.Contains(normalizeCoinpairFilter(candidate), filter) {
				return strings.ToUpper(strings.TrimSpace(keyword)), true
			}
		}
	}
	return "", false
}

// coinpairWhitelistMatch returns the first configured keyword that matches the
// signal. An empty whitelist allows every signal, while a non-empty whitelist
// only allows entry signals whose coinpair or ticker contains one keyword.
func coinpairWhitelistMatch(cfg config.Config, signal trading.Signal) (string, bool) {
	if strings.EqualFold(strings.TrimSpace(signal.PositionEffect), trading.PositionEffectClose) {
		return "", true
	}
	if len(cfg.Trading.CoinpairWhitelist) == 0 {
		return "", true
	}
	for _, keyword := range cfg.Trading.CoinpairWhitelist {
		filter := normalizeCoinpairFilter(keyword)
		if filter == "" {
			continue
		}
		for _, candidate := range []string{signal.Coinpair, signal.Ticker} {
			if strings.Contains(normalizeCoinpairFilter(candidate), filter) {
				return strings.ToUpper(strings.TrimSpace(keyword)), true
			}
		}
	}
	return strings.Join(cfg.Trading.CoinpairWhitelist, ", "), false
}

func normalizeCoinpairFilter(value string) string {
	var normalized strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(value)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			normalized.WriteRune(r)
		}
	}
	return normalized.String()
}

func applySignalSourceExchangeRouting(signal *trading.Signal, targetExchangeProvided bool) {
	if targetExchangeProvided {
		return
	}
	target, ok := trading.TargetExchangeFromSignalSource(signal.Exchange, signal.Ticker)
	if !ok {
		return
	}
	current := trading.NormalizeExchange(signal.TargetExchange)
	if current != target || target != trading.ExchangeOKX {
		signal.APIID = ""
	}
	signal.TargetExchange = target
}

func signalPreviewFromJSON(body []byte) trading.Signal {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return trading.Signal{}
	}
	var signal trading.Signal
	readString := func(name string) string {
		raw, ok := fields[name]
		if !ok {
			return ""
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		var n json.Number
		if err := json.Unmarshal(raw, &n); err == nil {
			return n.String()
		}
		return ""
	}
	readInt := func(name string) int {
		raw, ok := fields[name]
		if !ok {
			return 0
		}
		var i int
		if err := json.Unmarshal(raw, &i); err == nil {
			return i
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			if parsed, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
				return parsed
			}
		}
		return 0
	}
	readFloat := func(name string) trading.FlexibleFloat {
		raw, ok := fields[name]
		if !ok {
			return trading.FlexibleFloat{}
		}
		var f trading.FlexibleFloat
		if err := json.Unmarshal(raw, &f); err == nil {
			return f
		}
		return trading.FlexibleFloat{}
	}
	signal.Action = trading.Side(readString("action"))
	signal.APIID = readString("api_id")
	signal.TargetExchange = readString("target_exchange")
	signal.TradeEnv = readString("trade_env")
	signal.Coinpair = readString("coinpair")
	signal.Price = readFloat("price")
	signal.SentAt = readString("sent_at")
	signal.Time = readString("time")
	signal.Ticker = readString("ticker")
	signal.Exchange = readString("exchange")
	signal.Interval = readString("interval")
	signal.Condition = readString("condition")
	signal.Text = readString("text")
	signal.OrderIntent = readString("order_intent")
	signal.Intent = readString("intent")
	signal.PositionEffect = readString("position_effect")
	signal.PositionSide = readString("position_side")
	signal.Leverage = readInt("leverage")
	signal.Amount = readFloat("amount")
	signal.TokenNonce = readString("token_nonce")
	signal.Token = readString("token")
	signal.Normalize()
	return signal
}

func (s *Server) execute(signalID string, signal trading.Signal, cfg config.Config) {
	if s.Executor == nil {
		_ = s.Orders.MarkFailed(signalID, errors.New("executor is not configured"), s.now())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := s.Executor.ExecuteSignal(ctx, signal, cfg)
	result.SignalID = signalID
	if err != nil {
		if result.Partial && result.HasSubmittedOrders() {
			_ = s.Orders.MarkPartiallySubmitted(signalID, result, err, s.now())
		} else {
			_ = s.Orders.MarkFailed(signalID, err, s.now())
		}
		if s.Logger != nil {
			s.Logger.Error("order failed", "signal_id", signalID, "action", signal.Action, "coinpair", signal.Coinpair, "error", err)
		}
		return
	}
	_ = s.Orders.MarkSubmitted(signalID, result, s.now())
}

func (s *Server) handleTVBot(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/tvbot")
	if path == "/login" || path == "/login/" {
		if s.RedirectUnifiedLogin != nil {
			s.RedirectUnifiedLogin(w, r)
			return
		}
		s.handleTVBotLogin(w, r)
		return
	}
	if path == "/logout" || path == "/logout/" {
		if s.LogoutUnified != nil {
			s.LogoutUnified(w, r)
			return
		}
		s.handleTVBotLogout(w, r)
		return
	}
	if s.RequireUnifiedUser != nil {
		require := s.RequireUnifiedUser
		if requiresAdminEndpoint(path, r) {
			require = s.RequireUnifiedAdmin
		}
		if require == nil || !require(w, r) {
			return
		}
	} else if !s.requireAdmin(w, r) {
		return
	}
	if s.UserCredentialResolver != nil && s.CurrentUser != nil {
		username, ok := s.CurrentUser(r)
		if !ok || strings.TrimSpace(username) == "" {
			writeError(w, http.StatusUnauthorized, "authentication_required", "authenticated user identity is required")
			return
		}
		scoped, err := s.scopedForUser(username)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "user_credentials_unavailable", err.Error())
			return
		}
		s = scoped
	}
	if strings.HasPrefix(path, "/view/") {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "tvbot view pages only accept GET")
			return
		}
		if _, ok := tvbotViewRouteForPath(path); !ok {
			writeError(w, http.StatusNotFound, "not_found", "unknown /tvbot view")
			return
		}
		writeHTML(w, http.StatusOK, s.renderTVBotHTML(r))
		return
	}
	if path == "" || path == "/" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is allowed")
			return
		}
		writeHTML(w, http.StatusOK, s.renderTVBotHTML(r))
		return
	}
	switch {
	case path == "/config":
		s.handleConfig(w, r)
	case path == "/symbols":
		s.handleSymbols(w, r)
	case path == "/templates":
		s.handleTemplates(w, r)
	case path == "/orders":
		s.handleOrders(w, r)
	case path == "/coinpair-blocks":
		s.handleCoinpairBlocks(w, r)
	case strings.HasPrefix(path, "/coinpair-blocks/"):
		s.handleCoinpairBlockDelete(w, r, strings.TrimPrefix(path, "/coinpair-blocks/"))
	case path == "/trade-monitor":
		s.handleTradeMonitor(w, r)
	case isOrderRetryPath(path):
		s.handleOrderRetry(w, r, path)
	case path == "/analysis":
		s.handleAnalysis(w, r)
	case path == "/balances/overview":
		s.handleBalanceOverview(w, r)
	case path == "/position-target":
		s.handlePositionTarget(w, r)
	case path == "/positions":
		s.handlePositions(w, r)
	case path == "/pending-orders":
		s.handlePendingOrders(w, r)
	case path == "/pending-orders/risk":
		s.handlePendingOrderRisk(w, r)
	case path == "/pending-orders/chase":
		s.handlePendingOrderChase(w, r)
	case path == "/pending-orders/chase/stop":
		s.handlePendingOrderChaseStop(w, r)
	case path == "/pending-orders/cancel":
		s.handlePendingOrderCancel(w, r)
	case path == "/positions/close":
		s.handlePositionClose(w, r)
	case path == "/positions/protection":
		s.handlePositionProtection(w, r)
	case path == "/api-keys":
		s.handleAPIKeys(w, r)
	case path == "/api-keys/test":
		s.handleAPIKeyTest(w, r)
	case path == "/check-okx":
		s.handleCheckOKX(w, r)
	default:
		writeError(w, http.StatusNotFound, "not_found", "unknown /tvbot endpoint")
	}
}

func requiresAdminEndpoint(path string, r *http.Request) bool {
	if strings.HasPrefix(path, "/view/") {
		return false
	}
	if path == "/api-keys" || path == "/api-keys/test" {
		return false
	}
	if path == "/" || path == "" || path == "/positions" || path == "/analysis" || path == "/orders" || path == "/trade-monitor" || path == "/balances/overview" || path == "/pending-orders" || path == "/symbols" || path == "/config" {
		return r.Method != http.MethodGet || path == "/config" && r.Method != http.MethodGet || path == "/symbols" && r.Method != http.MethodGet
	}
	// Order actions, settings, menu changes, and upgrades are administrator-only.
	// API key endpoints are user-scoped and available to every authenticated user.
	return r.Method != http.MethodGet
}

func (s *Server) logTVOrderRejected(r *http.Request, signal trading.Signal, code string, err error) {
	if s.Logger == nil {
		return
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	s.Logger.Warn("tvorder rejected",
		"code", code,
		"message", msg,
		"client_ip", clientIP(r),
		"user_agent", r.UserAgent(),
		"action", signal.Action,
		"api_id", signal.APIID,
		"coinpair", signal.Coinpair,
		"ticker", signal.Ticker,
		"trade_env", signal.TradeEnv,
		"sent_at", signal.SentAt,
	)
}

func clientIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		if i := strings.Index(forwarded, ","); i >= 0 {
			return strings.TrimSpace(forwarded[:i])
		}
		return forwarded
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	return r.RemoteAddr
}

func (s *Server) handleAPIKeys(w http.ResponseWriter, r *http.Request) {
	exchange := trading.NormalizeExchange(r.URL.Query().Get("exchange"))
	defaultEnv := config.EnvDemo
	if s.ConfigStore != nil {
		defaultEnv = s.ConfigStore.Get().Trading.Env
	}
	env := normalizeCredentialEnvironment(r.URL.Query().Get("env"), defaultEnv)
	switch r.Method {
	case http.MethodGet:
		switch exchange {
		case trading.ExchangeBinance:
			store := s.binanceCredentialStore(env)
			if store == nil {
				writeError(w, http.StatusServiceUnavailable, "not_configured", "Binance credential store is not configured")
				return
			}
			writeJSON(w, http.StatusOK, store.Status())
		case trading.ExchangeGate:
			store := s.gateCredentialStore(env)
			if store == nil {
				writeError(w, http.StatusServiceUnavailable, "not_configured", "Gate credential store is not configured")
				return
			}
			writeJSON(w, http.StatusOK, store.Status())
		default:
			store := s.okxCredentialStore(env)
			if store == nil {
				writeError(w, http.StatusServiceUnavailable, "not_configured", "OKX credential store is not configured")
				return
			}
			writeJSON(w, http.StatusOK, store.Status())
		}
	case http.MethodPut:
		var req struct {
			Exchange   string `json:"exchange"`
			Env        string `json:"env"`
			ID         string `json:"id"`
			Name       string `json:"name"`
			APIKey     string `json:"api_key"`
			SecretKey  string `json:"secret_key"`
			Passphrase string `json:"passphrase"`
			Active     *bool  `json:"active"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
		if req.Exchange != "" {
			exchange = trading.NormalizeExchange(req.Exchange)
		}
		env = normalizeCredentialEnvironment(req.Env, defaultEnv)
		active := false
		if req.Active != nil {
			active = *req.Active
		}
		switch exchange {
		case trading.ExchangeBinance:
			store := s.binanceCredentialStore(env)
			if store == nil {
				writeError(w, http.StatusServiceUnavailable, "not_configured", "Binance credential store is not configured")
				return
			}
			existing := store.Status()
			id := strings.TrimSpace(req.ID)
			if id == "" {
				id = strings.TrimSpace(existing.ActiveID)
			}
			if id == "" && len(existing.Credentials) > 0 {
				id = existing.Credentials[0].ID
			}
			status, err := store.SaveSingleAccount(binance.CredentialAccountUpdate{
				ID:              id,
				Name:            req.Name,
				Active:          active,
				PreserveMissing: true,
				Credentials: binance.Credentials{
					APIKey:    req.APIKey,
					SecretKey: req.SecretKey,
				},
			})
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_api_keys", err.Error())
				return
			}
			if err := s.syncAPIKeyTradeTarget(exchange, env, status.ActiveID, active); err != nil {
				writeError(w, http.StatusInternalServerError, "save_trading_target", err.Error())
				return
			}
			status = store.Status()
			writeJSON(w, http.StatusOK, status)
			return
		case trading.ExchangeGate:
			store := s.gateCredentialStore(env)
			if store == nil {
				writeError(w, http.StatusServiceUnavailable, "not_configured", "Gate credential store is not configured")
				return
			}
			existing := store.Status()
			id := strings.TrimSpace(req.ID)
			if id == "" {
				id = strings.TrimSpace(existing.ActiveID)
			}
			if id == "" && len(existing.Credentials) > 0 {
				id = existing.Credentials[0].ID
			}
			status, err := store.SaveSingleAccount(gate.CredentialAccountUpdate{ID: id, Name: req.Name, Active: active, PreserveMissing: true, Credentials: gate.Credentials{APIKey: req.APIKey, SecretKey: req.SecretKey}})
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_api_keys", err.Error())
				return
			}
			if err := s.syncAPIKeyTradeTarget(exchange, env, status.ActiveID, active); err != nil {
				writeError(w, http.StatusInternalServerError, "save_trading_target", err.Error())
				return
			}
			status = store.Status()
			writeJSON(w, http.StatusOK, status)
			return
		default:
			store := s.okxCredentialStore(env)
			if store == nil {
				writeError(w, http.StatusServiceUnavailable, "not_configured", "OKX credential store is not configured")
				return
			}
			existing := store.Status()
			id := strings.TrimSpace(req.ID)
			if id == "" {
				id = strings.TrimSpace(existing.ActiveID)
			}
			if id == "" && len(existing.Credentials) > 0 {
				id = existing.Credentials[0].ID
			}
			status, err := store.SaveSingleAccount(okx.CredentialAccountUpdate{
				ID:              id,
				Name:            req.Name,
				Active:          active,
				PreserveMissing: true,
				Credentials: okx.Credentials{
					APIKey:     req.APIKey,
					SecretKey:  req.SecretKey,
					Passphrase: req.Passphrase,
				},
			})
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_api_keys", err.Error())
				return
			}
			if err := s.syncAPIKeyTradeTarget(exchange, env, status.ActiveID, active); err != nil {
				writeError(w, http.StatusInternalServerError, "save_trading_target", err.Error())
				return
			}
			status = store.Status()
			writeJSON(w, http.StatusOK, status)
		}
	case http.MethodPatch:
		var req struct {
			Exchange string `json:"exchange"`
			Env      string `json:"env"`
			ID       string `json:"id"`
			NewID    string `json:"new_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
		if req.Exchange != "" {
			exchange = trading.NormalizeExchange(req.Exchange)
		}
		env = normalizeCredentialEnvironment(req.Env, r.URL.Query().Get("env"))
		if strings.TrimSpace(req.Env) == "" && strings.TrimSpace(r.URL.Query().Get("env")) == "" {
			env = normalizeCredentialEnvironment(defaultEnv, defaultEnv)
		}
		id := strings.TrimSpace(req.ID)
		if id == "" {
			id = r.URL.Query().Get("id")
		}
		switch exchange {
		case trading.ExchangeBinance:
			store := s.binanceCredentialStore(env)
			if store == nil {
				writeError(w, http.StatusServiceUnavailable, "not_configured", "Binance credential store is not configured")
				return
			}
			status, err := store.RenameAccount(id, req.NewID)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_api_keys", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, status)
			return
		case trading.ExchangeGate:
			store := s.gateCredentialStore(env)
			if store == nil {
				writeError(w, http.StatusServiceUnavailable, "not_configured", "Gate credential store is not configured")
				return
			}
			status, err := store.RenameAccount(id, req.NewID)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_api_keys", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, status)
			return
		default:
			store := s.okxCredentialStore(env)
			if store == nil {
				writeError(w, http.StatusServiceUnavailable, "not_configured", "OKX credential store is not configured")
				return
			}
			status, err := store.RenameAccount(id, req.NewID)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_api_keys", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, status)
		}
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		switch exchange {
		case trading.ExchangeBinance:
			store := s.binanceCredentialStore(env)
			if store == nil {
				writeError(w, http.StatusServiceUnavailable, "not_configured", "Binance credential store is not configured")
				return
			}
			status, err := store.DeleteAccount(id)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_api_keys", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, status)
			return
		case trading.ExchangeGate:
			store := s.gateCredentialStore(env)
			if store == nil {
				writeError(w, http.StatusServiceUnavailable, "not_configured", "Gate credential store is not configured")
				return
			}
			status, err := store.DeleteAccount(id)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_api_keys", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, status)
			return
		default:
			store := s.okxCredentialStore(env)
			if store == nil {
				writeError(w, http.StatusServiceUnavailable, "not_configured", "OKX credential store is not configured")
				return
			}
			status, err := store.DeleteAccount(id)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_api_keys", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, status)
		}
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET, PUT, PATCH and DELETE are allowed")
	}
}

func (s *Server) handleAPIKeyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is allowed")
		return
	}
	var req struct {
		Exchange   string `json:"exchange"`
		Env        string `json:"env"`
		ID         string `json:"id"`
		APIKey     string `json:"api_key"`
		SecretKey  string `json:"secret_key"`
		Passphrase string `json:"passphrase"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	exchange := trading.NormalizeExchange(req.Exchange)
	defaultEnv := config.EnvDemo
	if s.ConfigStore != nil {
		defaultEnv = s.ConfigStore.Get().Trading.Env
	}
	env := normalizeCredentialEnvironment(req.Env, defaultEnv)
	cfg := configForTradeEnv(s.ConfigStore.Get(), env)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if exchange == trading.ExchangeGate {
		creds := gate.Credentials{APIKey: strings.TrimSpace(req.APIKey), SecretKey: strings.TrimSpace(req.SecretKey)}
		if gateCredentialFieldsProvided(creds) {
			store := s.gateCredentialStore(env)
			if store != nil {
				if stored, resolvedID, err := store.GateCredentials(req.ID); err == nil {
					req.ID = resolvedID
					mergeMissingGateCredentials(&creds, stored)
				}
			}
			result, err := checkGateCredentials(ctx, cfg, req.ID, creds)
			if err != nil {
				writeError(w, http.StatusBadGateway, "gate_check_failed", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
		result, err := s.checkStoredGate(ctx, cfg, req.ID)
		if err != nil {
			writeError(w, http.StatusBadGateway, "gate_check_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if exchange == trading.ExchangeBinance {
		creds := binance.Credentials{APIKey: strings.TrimSpace(req.APIKey), SecretKey: strings.TrimSpace(req.SecretKey)}
		if binanceCredentialFieldsProvided(creds) {
			store := s.binanceCredentialStore(env)
			if store != nil {
				if stored, resolvedID, err := store.BinanceCredentials(req.ID); err == nil {
					req.ID = resolvedID
					mergeMissingBinanceCredentials(&creds, stored)
				}
			}
			result, err := checkBinanceCredentials(ctx, cfg, req.ID, creds)
			if err != nil {
				writeError(w, http.StatusBadGateway, "binance_check_failed", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
		result, err := s.checkStoredBinance(ctx, cfg, req.ID)
		if err != nil {
			writeError(w, http.StatusBadGateway, "binance_check_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	creds := okx.Credentials{
		APIKey:     strings.TrimSpace(req.APIKey),
		SecretKey:  strings.TrimSpace(req.SecretKey),
		Passphrase: strings.TrimSpace(req.Passphrase),
	}
	if credentialFieldsProvided(creds) {
		store := s.okxCredentialStore(env)
		if store != nil {
			if stored, resolvedID, err := store.OKXCredentials(req.ID); err == nil {
				req.ID = resolvedID
				mergeMissingCredentials(&creds, stored)
			}
		}
		result, err := checkOKXCredentials(ctx, cfg, req.ID, creds)
		if err != nil {
			writeError(w, http.StatusBadGateway, "okx_check_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	result, err := s.checkStoredOKX(ctx, cfg, req.ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "okx_check_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	if s.RequireUnifiedAdmin != nil {
		if !s.RequireUnifiedAdmin(w, r) {
			return
		}
	} else if !s.requireAdmin(w, r) {
		return
	}
	if s.Upgrade == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "upgrade runner is not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.Upgrade.Status())
	case http.MethodPost:
		result, started, err := s.Upgrade.Start(context.Background())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "upgrade_error", err.Error())
			return
		}
		if started {
			writeJSON(w, http.StatusAccepted, result)
			return
		}
		writeJSON(w, http.StatusConflict, result)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET and POST are allowed")
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.ConfigStore.Get())
	case http.MethodPut, http.MethodPatch:
		var patch configPatch
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch); err != nil {
			writeError(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
		if err := validateConfigPatch(patch); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
			return
		}
		previous := s.ConfigStore.Get()
		cfg, err := s.ConfigStore.Update(func(c *config.Config) error {
			applyConfigPatch(c, patch)
			return nil
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
			return
		}
		if s.Orders != nil {
			if previous.Trading.LossCooldown.Enabled && !cfg.Trading.LossCooldown.Enabled {
				var clearErr error
				if cfg.Trading.LossCooldown.GlobalEnabled {
					_, clearErr = s.Orders.DeleteAutomaticCoinpairBlocksExcept(globalCoinpairCooldownKeyword)
				} else {
					_, clearErr = s.Orders.DeleteAutomaticCoinpairBlocks()
				}
				if clearErr != nil && s.Logger != nil {
					s.Logger.Error("failed to clear automatic coinpair cooldowns after disabling same-coinpair loss cooldown", "error", clearErr)
				}
			}
			if previous.Trading.LossCooldown.GlobalEnabled && !cfg.Trading.LossCooldown.GlobalEnabled {
				if _, clearErr := s.Orders.DeleteCoinpairBlock(globalCoinpairCooldownKeyword); clearErr != nil && s.Logger != nil {
					s.Logger.Error("failed to clear global loss cooldown", "error", clearErr)
				}
			}
		}
		writeJSON(w, http.StatusOK, cfg)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET, PUT and PATCH are allowed")
	}
}

func validateConfigPatch(patch configPatch) error {
	if patch.Trading == nil {
		return nil
	}
	if cooldown := patch.Trading.LossCooldown; cooldown != nil {
		if cooldown.CoinpairHours != nil && (*cooldown.CoinpairHours < 1 || *cooldown.CoinpairHours > 8760) {
			return errors.New("loss_cooldown.coinpair_hours must be between 1 and 8760")
		}
		if cooldown.GlobalHours != nil && (*cooldown.GlobalHours < 1 || *cooldown.GlobalHours > 8760) {
			return errors.New("loss_cooldown.global_hours must be between 1 and 8760")
		}
		if cooldown.Hours != nil && (*cooldown.Hours < 1 || *cooldown.Hours > 8760) {
			return errors.New("loss_cooldown.hours must be between 1 and 8760")
		}
	}
	if patch.Trading.PositionMonitor == nil {
		return nil
	}
	monitor := patch.Trading.PositionMonitor
	if monitor.PollIntervalSeconds <= 0 {
		return errors.New("position_monitor.poll_interval_seconds must be positive")
	}
	if monitor.TakeProfitPct <= 0 {
		return errors.New("position_monitor.take_profit_pct must be positive")
	}
	if monitor.StopLossPct <= 0 {
		return errors.New("position_monitor.stop_loss_pct must be positive")
	}
	return nil
}

func (s *Server) handleSymbols(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.ConfigStore.Get()
		resp, err := s.cachedSymbolsResponse(cfg)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "symbol_cache_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case http.MethodPost:
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		resp, err := s.syncSymbolCatalogs(ctx)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "symbol_sync_error", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case http.MethodPut:
		symbols, err := decodeSymbols(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
		cfg, err := s.ConfigStore.Update(func(c *config.Config) error {
			c.Symbols = symbols
			return nil
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_symbols", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"symbols": cfg.Symbols})
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET, POST and PUT are allowed")
	}
}

type symbolsResponse struct {
	Symbols map[string]config.SymbolConfig `json:"symbols"`
	OKX     okxSymbolsCatalog              `json:"okx"`
	Binance binanceSymbolsCatalog          `json:"binance"`
	Gate    gateSymbolsCatalog             `json:"gate"`
}

type okxSymbolsCatalog struct {
	Live okxInstrumentSet `json:"live"`
	Demo okxInstrumentSet `json:"demo"`
}

type binanceSymbolsCatalog struct {
	Live binanceInstrumentSet `json:"live"`
	Demo binanceInstrumentSet `json:"demo"`
}

type gateSymbolsCatalog struct {
	Live gateInstrumentSet `json:"live"`
	Demo gateInstrumentSet `json:"demo"`
}

type okxInstrumentSet struct {
	Env            string             `json:"env"`
	Demo           bool               `json:"demo"`
	Count          int                `json:"count"`
	Instruments    []symbolInstrument `json:"instruments"`
	TopInstruments []symbolInstrument `json:"top_instruments"`
	Error          string             `json:"error,omitempty"`
	TickerError    string             `json:"ticker_error,omitempty"`
	SyncedAt       string             `json:"synced_at,omitempty"`
	AttemptedAt    string             `json:"attempted_at,omitempty"`
}

type symbolInstrument struct {
	okx.Instrument
	TurnoverUSDT24h string `json:"turnover_usdt_24h,omitempty"`
	TickerUpdatedAt string `json:"ticker_updated_at,omitempty"`
}

type binanceInstrumentSet struct {
	Env            string                    `json:"env"`
	Demo           bool                      `json:"demo"`
	Count          int                       `json:"count"`
	Instruments    []binanceSymbolInstrument `json:"instruments"`
	TopInstruments []binanceSymbolInstrument `json:"top_instruments"`
	Error          string                    `json:"error,omitempty"`
	TickerError    string                    `json:"ticker_error,omitempty"`
	SyncedAt       string                    `json:"synced_at,omitempty"`
	AttemptedAt    string                    `json:"attempted_at,omitempty"`
}

type binanceSymbolInstrument struct {
	binance.SymbolInfo
	TickSize        string `json:"tick_size,omitempty"`
	StepSize        string `json:"step_size,omitempty"`
	MinQty          string `json:"min_qty,omitempty"`
	MaxQty          string `json:"max_qty,omitempty"`
	MinNotional     string `json:"min_notional,omitempty"`
	TurnoverUSDT24h string `json:"turnover_usdt_24h,omitempty"`
	TickerUpdatedAt string `json:"ticker_updated_at,omitempty"`
}

type gateInstrumentSet struct {
	Env            string                 `json:"env"`
	Demo           bool                   `json:"demo"`
	Count          int                    `json:"count"`
	Instruments    []gateSymbolInstrument `json:"instruments"`
	TopInstruments []gateSymbolInstrument `json:"top_instruments"`
	Error          string                 `json:"error,omitempty"`
	TickerError    string                 `json:"ticker_error,omitempty"`
	SyncedAt       string                 `json:"synced_at,omitempty"`
	AttemptedAt    string                 `json:"attempted_at,omitempty"`
}

type gateSymbolInstrument struct {
	Symbol          string `json:"symbol"`
	InstID          string `json:"instId"`
	BaseAsset       string `json:"baseAsset"`
	QuoteAsset      string `json:"quoteAsset"`
	BaseCcy         string `json:"baseCcy"`
	QuoteCcy        string `json:"quoteCcy"`
	SettleCcy       string `json:"settleCcy"`
	MarginAsset     string `json:"marginAsset"`
	State           string `json:"state"`
	Status          string `json:"status"`
	Type            string `json:"type"`
	CtVal           string `json:"ctVal"`
	CtValCcy        string `json:"ctValCcy"`
	MinQty          string `json:"min_qty,omitempty"`
	MaxQty          string `json:"max_qty,omitempty"`
	StepSize        string `json:"step_size,omitempty"`
	TickSize        string `json:"tick_size,omitempty"`
	TurnoverUSDT24h string `json:"turnover_usdt_24h,omitempty"`
	TickerUpdatedAt string `json:"ticker_updated_at,omitempty"`
}

type symbolInstrumentResult struct {
	exchange   string
	env        string
	okxSet     okxInstrumentSet
	binanceSet binanceInstrumentSet
	gateSet    gateInstrumentSet
}

func (s *Server) StartSymbolCatalogSyncer(ctx context.Context) {
	if s.ConfigStore == nil || s.Orders == nil {
		return
	}
	go s.runSymbolCatalogSyncer(ctx, symbolCatalogSyncInterval)
}

func (s *Server) runSymbolCatalogSyncer(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = symbolCatalogSyncInterval
	}
	s.syncSymbolCatalogsWithLog(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.syncSymbolCatalogsWithLog(ctx)
		}
	}
}

func (s *Server) syncSymbolCatalogsWithLog(ctx context.Context) {
	syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := s.syncSymbolCatalogs(syncCtx); err != nil && s.Logger != nil {
		s.Logger.Warn("failed to sync symbol catalog cache", "error", err)
	}
}

func (s *Server) cachedSymbolsResponse(cfg config.Config) (symbolsResponse, error) {
	resp := emptySymbolsResponse(cfg.Symbols)
	if s.Orders == nil {
		return resp, errors.New("order store is not configured")
	}
	items, err := s.Orders.ListSymbolCatalogCaches()
	if err != nil {
		return resp, err
	}
	for _, item := range items {
		switch item.Exchange {
		case trading.ExchangeBinance:
			var set binanceInstrumentSet
			if err := json.Unmarshal([]byte(item.PayloadJSON), &set); err != nil {
				return resp, err
			}
			applyBinanceSetCacheMeta(&set, item)
			if item.Env == config.EnvLive {
				resp.Binance.Live = set
			} else {
				resp.Binance.Demo = set
			}
		case trading.ExchangeOKX:
			var set okxInstrumentSet
			if err := json.Unmarshal([]byte(item.PayloadJSON), &set); err != nil {
				return resp, err
			}
			applyOKXSetCacheMeta(&set, item)
			if item.Env == config.EnvLive {
				resp.OKX.Live = set
			} else {
				resp.OKX.Demo = set
			}
		case trading.ExchangeGate:
			var set gateInstrumentSet
			if err := json.Unmarshal([]byte(item.PayloadJSON), &set); err != nil {
				return resp, err
			}
			applyGateSetCacheMeta(&set, item)
			if item.Env == config.EnvLive {
				resp.Gate.Live = set
			} else {
				resp.Gate.Demo = set
			}
		}
	}
	applyMarketScopeRankings(&resp, cfg.Trading.MarketTurnoverScope)
	markUnavailableMarketScopeRankings(&resp, cfg.Trading.MarketTurnoverScope)
	return resp, nil
}

func (s *Server) syncSymbolCatalogs(ctx context.Context) (symbolsResponse, error) {
	if s.ConfigStore == nil || s.Orders == nil {
		return symbolsResponse{}, errors.New("server stores are not configured")
	}
	cfg := s.ConfigStore.Get()
	resp := s.fetchSymbolCatalogs(ctx, cfg)
	applyMarketScopeRankings(&resp, cfg.Trading.MarketTurnoverScope)
	markUnavailableMarketScopeRankings(&resp, cfg.Trading.MarketTurnoverScope)
	now := s.now()
	items, err := symbolCatalogCacheItems(resp, now)
	if err != nil {
		return resp, err
	}
	previous, err := s.Orders.ListSymbolCatalogCaches()
	if err != nil {
		return resp, err
	}
	items = preserveSuccessfulSymbolCatalogCaches(items, previous)
	if err := s.Orders.UpsertSymbolCatalogCaches(items); err != nil {
		return resp, err
	}
	return s.cachedSymbolsResponse(cfg)
}

func (s *Server) fetchSymbolCatalogs(ctx context.Context, cfg config.Config) symbolsResponse {
	results := make(chan symbolInstrumentResult, 6)
	go func() {
		results <- symbolInstrumentResult{exchange: trading.ExchangeOKX, env: config.EnvLive, okxSet: s.fetchOKXInstrumentSet(ctx, cfg, false)}
	}()
	go func() {
		results <- symbolInstrumentResult{exchange: trading.ExchangeOKX, env: config.EnvDemo, okxSet: s.fetchOKXInstrumentSet(ctx, cfg, true)}
	}()
	go func() {
		results <- symbolInstrumentResult{exchange: trading.ExchangeBinance, env: config.EnvLive, binanceSet: s.fetchBinanceInstrumentSet(ctx, cfg, false)}
	}()
	go func() {
		results <- symbolInstrumentResult{exchange: trading.ExchangeBinance, env: config.EnvDemo, binanceSet: s.fetchBinanceInstrumentSet(ctx, cfg, true)}
	}()
	go func() {
		results <- symbolInstrumentResult{exchange: trading.ExchangeGate, env: config.EnvLive, gateSet: s.fetchGateInstrumentSet(ctx, cfg, false)}
	}()
	go func() {
		results <- symbolInstrumentResult{exchange: trading.ExchangeGate, env: config.EnvDemo, gateSet: s.fetchGateInstrumentSet(ctx, cfg, true)}
	}()
	resp := emptySymbolsResponse(cfg.Symbols)
	for i := 0; i < 6; i++ {
		result := <-results
		if result.exchange == trading.ExchangeBinance {
			if result.env == config.EnvDemo {
				resp.Binance.Demo = result.binanceSet
				continue
			}
			resp.Binance.Live = result.binanceSet
			continue
		}
		if result.exchange == trading.ExchangeGate {
			if result.env == config.EnvDemo {
				resp.Gate.Demo = result.gateSet
				continue
			}
			resp.Gate.Live = result.gateSet
			continue
		}
		if result.env == config.EnvDemo {
			resp.OKX.Demo = result.okxSet
			continue
		}
		resp.OKX.Live = result.okxSet
	}
	applyMarketScopeRankings(&resp, cfg.Trading.MarketTurnoverScope)
	return resp
}

func emptySymbolsResponse(symbols map[string]config.SymbolConfig) symbolsResponse {
	return symbolsResponse{
		Symbols: symbols,
		OKX: okxSymbolsCatalog{
			Live: okxInstrumentSet{Env: config.EnvLive, Demo: false, Instruments: []symbolInstrument{}},
			Demo: okxInstrumentSet{Env: config.EnvDemo, Demo: true, Instruments: []symbolInstrument{}},
		},
		Binance: binanceSymbolsCatalog{
			Live: binanceInstrumentSet{Env: config.EnvLive, Demo: false, Instruments: []binanceSymbolInstrument{}},
			Demo: binanceInstrumentSet{Env: config.EnvDemo, Demo: true, Instruments: []binanceSymbolInstrument{}},
		},
		Gate: gateSymbolsCatalog{
			Live: gateInstrumentSet{Env: config.EnvLive, Demo: false, Instruments: []gateSymbolInstrument{}},
			Demo: gateInstrumentSet{Env: config.EnvDemo, Demo: true, Instruments: []gateSymbolInstrument{}},
		},
	}
}

func symbolCatalogCacheItems(resp symbolsResponse, now time.Time) ([]storage.SymbolCatalogCache, error) {
	now = now.UTC()
	sets := []struct {
		exchange string
		env      string
		okx      *okxInstrumentSet
		binance  *binanceInstrumentSet
		gate     *gateInstrumentSet
	}{
		{exchange: trading.ExchangeOKX, env: config.EnvLive, okx: &resp.OKX.Live},
		{exchange: trading.ExchangeOKX, env: config.EnvDemo, okx: &resp.OKX.Demo},
		{exchange: trading.ExchangeBinance, env: config.EnvLive, binance: &resp.Binance.Live},
		{exchange: trading.ExchangeBinance, env: config.EnvDemo, binance: &resp.Binance.Demo},
		{exchange: trading.ExchangeGate, env: config.EnvLive, gate: &resp.Gate.Live},
		{exchange: trading.ExchangeGate, env: config.EnvDemo, gate: &resp.Gate.Demo},
	}
	items := make([]storage.SymbolCatalogCache, 0, len(sets))
	for _, entry := range sets {
		var payload []byte
		var count int
		var errorText, tickerError string
		if entry.okx != nil {
			applyOKXSetSyncMeta(entry.okx, now)
			var err error
			payload, err = json.Marshal(entry.okx)
			if err != nil {
				return nil, err
			}
			count = entry.okx.Count
			errorText = entry.okx.Error
			tickerError = entry.okx.TickerError
		} else if entry.binance != nil {
			applyBinanceSetSyncMeta(entry.binance, now)
			var err error
			payload, err = json.Marshal(entry.binance)
			if err != nil {
				return nil, err
			}
			count = entry.binance.Count
			errorText = entry.binance.Error
			tickerError = entry.binance.TickerError
		} else {
			applyGateSetSyncMeta(entry.gate, now)
			var err error
			payload, err = json.Marshal(entry.gate)
			if err != nil {
				return nil, err
			}
			count = entry.gate.Count
			errorText = entry.gate.Error
			tickerError = entry.gate.TickerError
		}
		item := storage.SymbolCatalogCache{
			Exchange:    entry.exchange,
			Env:         entry.env,
			PayloadJSON: string(payload),
			Count:       count,
			AttemptedAt: now,
			Error:       errorText,
			TickerError: tickerError,
		}
		if strings.TrimSpace(errorText) == "" && strings.TrimSpace(tickerError) == "" {
			item.SyncedAt = now
		}
		items = append(items, item)
	}
	return items, nil
}

func preserveSuccessfulSymbolCatalogCaches(fetched, previous []storage.SymbolCatalogCache) []storage.SymbolCatalogCache {
	previousByMarket := make(map[string]storage.SymbolCatalogCache, len(previous))
	for _, item := range previous {
		previousByMarket[symbolCatalogMarketKey(item.Exchange, item.Env)] = item
	}
	out := make([]storage.SymbolCatalogCache, 0, len(fetched))
	for _, item := range fetched {
		if !item.SyncedAt.IsZero() {
			out = append(out, item)
			continue
		}
		old, ok := previousByMarket[symbolCatalogMarketKey(item.Exchange, item.Env)]
		if !ok || old.SyncedAt.IsZero() {
			out = append(out, item)
			continue
		}
		item.PayloadJSON = old.PayloadJSON
		item.Count = old.Count
		item.SyncedAt = old.SyncedAt
		out = append(out, item)
	}
	return out
}

func symbolCatalogMarketKey(exchange, env string) string {
	return trading.NormalizeExchange(exchange) + ":" + trading.NormalizeTradeEnv(env)
}

func applyOKXSetSyncMeta(set *okxInstrumentSet, now time.Time) {
	if set == nil {
		return
	}
	set.AttemptedAt = now.UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(set.Error) == "" && strings.TrimSpace(set.TickerError) == "" && len(set.TopInstruments) > 0 {
		set.SyncedAt = set.AttemptedAt
	}
}

func applyBinanceSetSyncMeta(set *binanceInstrumentSet, now time.Time) {
	if set == nil {
		return
	}
	set.AttemptedAt = now.UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(set.Error) == "" && strings.TrimSpace(set.TickerError) == "" && len(set.TopInstruments) > 0 {
		set.SyncedAt = set.AttemptedAt
	}
}

func applyGateSetSyncMeta(set *gateInstrumentSet, now time.Time) {
	if set == nil {
		return
	}
	set.AttemptedAt = now.UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(set.Error) == "" && strings.TrimSpace(set.TickerError) == "" && len(set.TopInstruments) > 0 {
		set.SyncedAt = set.AttemptedAt
	}
}

func applyOKXSetCacheMeta(set *okxInstrumentSet, item storage.SymbolCatalogCache) {
	if set.Instruments == nil {
		set.Instruments = []symbolInstrument{}
	}
	set.Env = item.Env
	set.Demo = item.Env == config.EnvDemo
	set.Count = item.Count
	set.Error = item.Error
	set.TickerError = item.TickerError
	if !item.SyncedAt.IsZero() {
		set.SyncedAt = item.SyncedAt.UTC().Format(time.RFC3339Nano)
	}
	if !item.AttemptedAt.IsZero() {
		set.AttemptedAt = item.AttemptedAt.UTC().Format(time.RFC3339Nano)
	}
}

func applyBinanceSetCacheMeta(set *binanceInstrumentSet, item storage.SymbolCatalogCache) {
	if set.Instruments == nil {
		set.Instruments = []binanceSymbolInstrument{}
	}
	set.Env = item.Env
	set.Demo = item.Env == config.EnvDemo
	set.Count = item.Count
	set.Error = item.Error
	set.TickerError = item.TickerError
	if !item.SyncedAt.IsZero() {
		set.SyncedAt = item.SyncedAt.UTC().Format(time.RFC3339Nano)
	}
	if !item.AttemptedAt.IsZero() {
		set.AttemptedAt = item.AttemptedAt.UTC().Format(time.RFC3339Nano)
	}
}

func applyGateSetCacheMeta(set *gateInstrumentSet, item storage.SymbolCatalogCache) {
	if set.Instruments == nil {
		set.Instruments = []gateSymbolInstrument{}
	}
	if set.TopInstruments == nil {
		set.TopInstruments = []gateSymbolInstrument{}
	}
	set.Env = item.Env
	set.Demo = item.Env == config.EnvDemo
	set.Count = item.Count
	set.Error = item.Error
	set.TickerError = item.TickerError
	if !item.SyncedAt.IsZero() {
		set.SyncedAt = item.SyncedAt.UTC().Format(time.RFC3339Nano)
	}
	if !item.AttemptedAt.IsZero() {
		set.AttemptedAt = item.AttemptedAt.UTC().Format(time.RFC3339Nano)
	}
}

func (s *Server) fetchOKXInstrumentSet(ctx context.Context, cfg config.Config, demo bool) okxInstrumentSet {
	env := config.EnvLive
	if demo {
		env = config.EnvDemo
	}
	set := okxInstrumentSet{
		Env:         env,
		Demo:        demo,
		Instruments: []symbolInstrument{},
	}
	client := okx.Client{
		BaseURL:    cfg.OKXBaseURL(),
		Demo:       demo,
		HTTPClient: s.OKXHTTPClient,
	}
	instruments, _, err := client.SwapInstruments(ctx)
	if err != nil {
		set.Error = err.Error()
		return set
	}
	instruments = filterOKXUSDTInstruments(instruments)
	sort.Slice(instruments, func(i, j int) bool {
		return strings.Compare(instruments[i].InstID, instruments[j].InstID) < 0
	})
	tickers, _, err := client.MarketTickers(ctx, "SWAP")
	if err != nil {
		set.TickerError = err.Error()
	}
	set.Instruments = symbolInstrumentsWithTickers(instruments, tickers)
	set.Count = len(instruments)
	return set
}

func symbolInstrumentsWithTickers(instruments []okx.Instrument, tickers []okx.Ticker) []symbolInstrument {
	tickerByInstID := make(map[string]okx.Ticker, len(tickers))
	for _, ticker := range tickers {
		instID := strings.ToUpper(strings.TrimSpace(ticker.InstID))
		if instID != "" {
			tickerByInstID[instID] = ticker
		}
	}
	out := make([]symbolInstrument, 0, len(instruments))
	for _, inst := range instruments {
		view := symbolInstrument{Instrument: inst}
		if ticker, ok := tickerByInstID[strings.ToUpper(strings.TrimSpace(inst.InstID))]; ok {
			view.TurnoverUSDT24h = tickerTurnoverUSDT24h(ticker)
			view.TickerUpdatedAt = tickerUpdatedAt(ticker)
		}
		out = append(out, view)
	}
	return out
}

func tickerTurnoverUSDT24h(ticker okx.Ticker) string {
	vol, volOK := parseAnyFloat(ticker.VolCcy24h)
	last, lastOK := parseAnyFloat(ticker.Last)
	if !volOK || !lastOK || vol < 0 || last < 0 {
		return ""
	}
	return trading.NormalizeFloat(vol * last)
}

func tickerUpdatedAt(ticker okx.Ticker) string {
	ts, err := strconv.ParseInt(strings.TrimSpace(ticker.TS), 10, 64)
	if err != nil || ts <= 0 {
		return ""
	}
	return time.UnixMilli(ts).UTC().Format(time.RFC3339Nano)
}

func (s *Server) fetchBinanceInstrumentSet(ctx context.Context, cfg config.Config, demo bool) binanceInstrumentSet {
	env := config.EnvLive
	if demo {
		env = config.EnvDemo
	}
	set := binanceInstrumentSet{
		Env:         env,
		Demo:        demo,
		Instruments: []binanceSymbolInstrument{},
	}
	client := binance.Client{
		BaseURL:    binanceCatalogBaseURL(cfg, demo),
		HTTPClient: s.binanceHTTPClient(),
	}
	info, err := client.ExchangeInfo(ctx)
	if err != nil {
		set.Error = err.Error()
		return set
	}
	symbols := filterBinanceUSDTPerpetualSymbols(info.Symbols)
	sort.Slice(symbols, func(i, j int) bool {
		return strings.Compare(symbols[i].Symbol, symbols[j].Symbol) < 0
	})
	tickers, err := client.Ticker24hr(ctx)
	if err != nil {
		set.TickerError = err.Error()
	}
	set.Instruments = binanceSymbolInstrumentsWithTickers(symbols, tickers)
	set.Count = len(symbols)
	return set
}

func (s *Server) fetchGateInstrumentSet(ctx context.Context, cfg config.Config, demo bool) gateInstrumentSet {
	env := config.EnvLive
	baseURL := cfg.Trading.GateBaseURL
	if demo {
		env = config.EnvDemo
		baseURL = cfg.Trading.GateDemoBaseURL
	}
	set := gateInstrumentSet{Env: env, Demo: demo, Instruments: []gateSymbolInstrument{}}
	client := gate.Client{BaseURL: baseURL, HTTPClient: s.GateHTTPClient}
	contracts, err := client.Contracts(ctx)
	if err != nil {
		set.Error = err.Error()
		return set
	}
	tickers, err := client.Tickers(ctx)
	if err != nil {
		set.TickerError = err.Error()
	}
	contracts = filterGateUSDTContracts(contracts)
	sort.Slice(contracts, func(i, j int) bool {
		return strings.Compare(strings.ToUpper(strings.TrimSpace(contracts[i].Name)), strings.ToUpper(strings.TrimSpace(contracts[j].Name))) < 0
	})
	set.Instruments = gateSymbolInstrumentsWithTickers(contracts, tickers)
	set.Count = len(set.Instruments)
	return set
}

func filterGateUSDTContracts(contracts []gate.Contract) []gate.Contract {
	filtered := make([]gate.Contract, 0, len(contracts))
	for _, contract := range contracts {
		name := strings.ToUpper(strings.TrimSpace(contract.Name))
		if !strings.HasSuffix(name, "_USDT") {
			continue
		}
		filtered = append(filtered, contract)
	}
	return filtered
}

func gateSymbolInstrumentsWithTickers(contracts []gate.Contract, tickers []gate.Ticker) []gateSymbolInstrument {
	tickerByContract := make(map[string]gate.Ticker, len(tickers))
	for _, ticker := range tickers {
		name := strings.ToUpper(strings.TrimSpace(ticker.Contract))
		if name != "" {
			tickerByContract[name] = ticker
		}
	}
	out := make([]gateSymbolInstrument, 0, len(contracts))
	for _, contract := range contracts {
		name := strings.ToUpper(strings.TrimSpace(contract.Name))
		base, quote := gateContractAssets(name)
		view := gateSymbolInstrument{
			Symbol: name, InstID: name, BaseAsset: base, QuoteAsset: quote,
			BaseCcy: base, QuoteCcy: quote, SettleCcy: quote, MarginAsset: quote,
			State: strings.ToLower(strings.TrimSpace(contract.Status)), Status: strings.TrimSpace(contract.Status),
			Type: contract.Type, CtVal: contract.QuantoMultiplier, CtValCcy: base,
			MinQty: contract.OrderSizeMin, MaxQty: contract.OrderSizeMax,
			StepSize: "1", TickSize: contract.OrderPriceRound,
		}
		if contract.EnableDecimal {
			view.StepSize = contract.OrderSizeMin
		}
		if ticker, ok := tickerByContract[name]; ok {
			view.TurnoverUSDT24h = trading.NormalizeFloat(parseGateTickerTurnover(ticker))
		}
		out = append(out, view)
	}
	return out
}

func gateContractAssets(name string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(name), "_", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(name), "USDT"
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func parseGateTickerTurnover(ticker gate.Ticker) float64 {
	value, ok := parseAnyFloat(ticker.Volume24hQuote)
	if !ok || value < 0 {
		return 0
	}
	return value
}

func binanceCatalogBaseURL(cfg config.Config, demo bool) string {
	if demo {
		return cfg.Trading.BinanceDemoBaseURL
	}
	return cfg.Trading.BinanceBaseURL
}

func binanceSymbolInstrumentsWithTickers(symbols []binance.SymbolInfo, tickers []binance.Ticker24hr) []binanceSymbolInstrument {
	tickerBySymbol := make(map[string]binance.Ticker24hr, len(tickers))
	for _, ticker := range tickers {
		symbol := strings.ToUpper(strings.TrimSpace(ticker.Symbol))
		if symbol != "" {
			tickerBySymbol[symbol] = ticker
		}
	}
	out := make([]binanceSymbolInstrument, 0, len(symbols))
	for _, symbol := range symbols {
		view := binanceSymbolInstrument{SymbolInfo: symbol}
		applyBinanceSymbolFilters(&view)
		if ticker, ok := tickerBySymbol[strings.ToUpper(strings.TrimSpace(symbol.Symbol))]; ok {
			view.TurnoverUSDT24h = binanceTickerTurnoverUSDT24h(ticker)
			view.TickerUpdatedAt = binanceTickerUpdatedAt(ticker)
		}
		out = append(out, view)
	}
	return out
}

func applyBinanceSymbolFilters(view *binanceSymbolInstrument) {
	for _, filter := range view.Filters {
		switch strings.ToUpper(strings.TrimSpace(filter.FilterType)) {
		case "PRICE_FILTER":
			view.TickSize = strings.TrimSpace(filter.TickSize)
		case "LOT_SIZE":
			view.StepSize = strings.TrimSpace(filter.StepSize)
			view.MinQty = strings.TrimSpace(filter.MinQty)
			view.MaxQty = strings.TrimSpace(filter.MaxQty)
		case "MARKET_LOT_SIZE":
			if view.StepSize == "" {
				view.StepSize = strings.TrimSpace(filter.StepSize)
			}
			if view.MinQty == "" {
				view.MinQty = strings.TrimSpace(filter.MinQty)
			}
			if view.MaxQty == "" {
				view.MaxQty = strings.TrimSpace(filter.MaxQty)
			}
		case "MIN_NOTIONAL", "NOTIONAL":
			view.MinNotional = strings.TrimSpace(filter.Notional)
		}
	}
}

func binanceTickerTurnoverUSDT24h(ticker binance.Ticker24hr) string {
	quoteVolume, ok := parseAnyFloat(ticker.QuoteVolume)
	if !ok || quoteVolume < 0 {
		return ""
	}
	return trading.NormalizeFloat(quoteVolume)
}

func binanceTickerUpdatedAt(ticker binance.Ticker24hr) string {
	if ticker.CloseTime <= 0 {
		return ""
	}
	return time.UnixMilli(ticker.CloseTime).UTC().Format(time.RFC3339Nano)
}

func filterOKXUSDTInstruments(instruments []okx.Instrument) []okx.Instrument {
	filtered := make([]okx.Instrument, 0, len(instruments))
	for _, inst := range instruments {
		if okxUSDTInstrument(inst) {
			filtered = append(filtered, inst)
		}
	}
	return filtered
}

func okxUSDTInstrument(inst okx.Instrument) bool {
	if okxUSDCInstrument(inst) {
		return false
	}
	quote := strings.ToUpper(strings.TrimSpace(inst.QuoteCcy))
	settle := strings.ToUpper(strings.TrimSpace(inst.SettleCcy))
	if quote != "" || settle != "" {
		return quote == "USDT" || settle == "USDT"
	}
	instID := strings.ToUpper(strings.TrimSpace(inst.InstID))
	return strings.Contains(instID, "-USDT-") || strings.HasSuffix(instID, "-USDT")
}

func okxUSDCInstrument(inst okx.Instrument) bool {
	for _, value := range []string{
		inst.InstID,
		inst.Uly,
		inst.InstFamily,
		inst.BaseCcy,
		inst.QuoteCcy,
		inst.SettleCcy,
	} {
		if strings.Contains(strings.ToUpper(strings.TrimSpace(value)), "USDC") {
			return true
		}
	}
	return false
}

func filterBinanceUSDTPerpetualSymbols(symbols []binance.SymbolInfo) []binance.SymbolInfo {
	filtered := make([]binance.SymbolInfo, 0, len(symbols))
	for _, symbol := range symbols {
		if binanceUSDTPerpetualSymbol(symbol) {
			filtered = append(filtered, symbol)
		}
	}
	return filtered
}

func binanceUSDTPerpetualSymbol(symbol binance.SymbolInfo) bool {
	if binanceUSDCSymbol(symbol) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(symbol.ContractType), "PERPETUAL") &&
		strings.EqualFold(strings.TrimSpace(symbol.QuoteAsset), "USDT") &&
		strings.EqualFold(strings.TrimSpace(symbol.MarginAsset), "USDT")
}

func binanceUSDCSymbol(symbol binance.SymbolInfo) bool {
	for _, value := range []string{
		symbol.Symbol,
		symbol.Pair,
		symbol.BaseAsset,
		symbol.QuoteAsset,
		symbol.MarginAsset,
	} {
		if strings.Contains(strings.ToUpper(strings.TrimSpace(value)), "USDC") {
			return true
		}
	}
	return false
}

func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is allowed")
		return
	}
	var req trading.TemplateRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", err.Error())
		return
	}
	req.TenantID = s.TenantID
	resp, err := trading.BuildTemplate(req, s.Token)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_template", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET is allowed")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			offset = parsed
		}
	}
	exchange := strings.TrimSpace(r.URL.Query().Get("exchange"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	status := storage.OrderStatus(strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status"))))
	if exchange != "" {
		if !trading.ValidTargetExchange(exchange) {
			writeError(w, http.StatusBadRequest, "invalid_exchange", "exchange must be okx or binance")
			return
		}
	}
	if status != "" && !storage.ValidOrderStatus(status) {
		writeError(w, http.StatusBadRequest, "invalid_status", "status must be accepted, duplicate, submitted, failed, rejected, or ignored")
		return
	}
	orders := s.Orders.ListFilteredPage(exchange, status, query, limit, offset)
	total := s.Orders.CountFiltered(exchange, status, query)
	totalPages := 0
	if limit > 0 && total > 0 {
		totalPages = (total + limit - 1) / limit
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"orders":      orders,
		"total":       total,
		"limit":       limit,
		"offset":      offset,
		"page":        offset/limit + 1,
		"page_size":   limit,
		"total_pages": totalPages,
	})
}

func isOrderRetryPath(path string) bool {
	rest := strings.TrimPrefix(path, "/orders/")
	return rest != path && strings.HasSuffix(rest, "/retry") && strings.TrimSuffix(rest, "/retry") != ""
}

func orderRetrySignalID(path string) string {
	rest := strings.TrimPrefix(path, "/orders/")
	return strings.TrimSuffix(rest, "/retry")
}

func (s *Server) handleOrderRetry(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is allowed")
		return
	}
	if s.Orders == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "order store is not configured")
		return
	}
	sourceID := orderRetrySignalID(path)
	source, ok := s.Orders.Get(sourceID)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "order record not found")
		return
	}
	if source.Status != storage.StatusFailed && source.Status != storage.StatusIgnored {
		writeError(w, http.StatusConflict, "not_retriable", "only failed or ignored orders can be retried")
		return
	}
	if message := unsupportedOKXInstrumentMessage(source); message != "" {
		writeError(w, http.StatusConflict, "not_retriable", message)
		return
	}
	if source.Status == storage.StatusIgnored && !ignoredOrderCanRetry(source) {
		writeError(w, http.StatusConflict, "not_retriable", fmt.Sprintf("ignored order reason %q cannot be safely retried", source.ErrorCode))
		return
	}
	cfg := configForTradeEnv(s.ConfigStore.Get(), source.TradeEnv)
	now := s.now()
	probe := trading.Signal{
		TargetExchange: source.TargetExchange,
		TradeEnv:       orderRecordTradeEnv(source),
		Coinpair:       source.Coinpair,
		Ticker:         source.Ticker,
		PositionEffect: source.PositionEffect,
	}
	if block, blocked, err := s.activeCoinpairCooldown(probe, now); err != nil {
		writeError(w, http.StatusServiceUnavailable, "cooldown_check_failed", err.Error())
		return
	} else if blocked {
		signal := ignoredRetrySignalFromRecord(source, now)
		record, err := s.recordCooldownIgnoredSignal(signal, block, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		resp := coinpairCooldownResponse(record, block)
		resp["retry_of"] = source.SignalID
		writeJSON(w, http.StatusAccepted, resp)
		return
	}
	if filter, ignored := ignoredEntrySignal(cfg, probe); ignored {
		signal := ignoredRetrySignalFromRecord(source, now)
		record, err := s.Orders.RecordIgnored(signal, filter, now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":    "ignored",
			"reason":    "coinpair_filtered",
			"signal_id": record.SignalID,
			"retry_of":  source.SignalID,
		})
		return
	}
	if filter, matched := coinpairWhitelistMatch(cfg, probe); !matched {
		signal := ignoredRetrySignalFromRecord(source, now)
		record, err := s.Orders.RecordIgnoredReason(signal, "coinpair_whitelist", fmt.Sprintf("coinpair did not match coinpair whitelist %q", filter), now)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "store_error", err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":    "ignored",
			"reason":    "coinpair_whitelist",
			"signal_id": record.SignalID,
			"retry_of":  source.SignalID,
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	price, err := s.currentRetryMarketPrice(ctx, cfg, source)
	if err != nil {
		if trading.NormalizeExchange(source.TargetExchange) == trading.ExchangeOKX && isOKXInstrumentNotFoundError(err) {
			message := formatUnsupportedOKXInstrumentMessage(source)
			if markErr := s.Orders.MarkFailedCode(source.SignalID, "okx_instrument_unsupported", errors.New(message), now); markErr != nil && s.Logger != nil {
				s.Logger.Error("failed to mark unsupported OKX instrument", "signal_id", source.SignalID, "error", markErr)
			}
			writeError(w, http.StatusConflict, "not_retriable", message)
			return
		}
		writeError(w, http.StatusBadGateway, "retry_price_failed", err.Error())
		return
	}
	signal, err := retrySignalFromRecord(source, cfg, now, price)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_retry_signal", err.Error())
		return
	}
	executeCfg := retryExecutionConfig(cfg, source)
	record, duplicate, err := s.Orders.RecordAccepted(signal, storage.RetryKey(source.SignalID, signal, now), now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if duplicate {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"status":    "duplicate",
			"signal_id": record.SignalID,
			"retry_of":  source.SignalID,
		})
		return
	}
	go s.execute(record.SignalID, signal, executeCfg)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":    "accepted",
		"signal_id": record.SignalID,
		"retry_of":  source.SignalID,
		"price":     trading.NormalizeFloat(signal.Price.Value),
	})
}

func ignoredOrderCanRetry(rec storage.OrderRecord) bool {
	code := strings.ToLower(strings.TrimSpace(rec.ErrorCode))
	return strings.HasPrefix(code, "outside_market_") || code == "coinpair_filtered" || code == "coinpair_cooldown" || code == "coinpair_whitelist"
}

func unsupportedOKXInstrumentMessage(rec storage.OrderRecord) string {
	if trading.NormalizeExchange(rec.TargetExchange) != trading.ExchangeOKX {
		return ""
	}
	details := strings.ToLower(strings.Join([]string{
		rec.ErrorCode,
		rec.Error,
		rec.Result.OKXCode,
		rec.Result.OKXMsg,
	}, " "))
	if !strings.Contains(details, "okx_instrument_unsupported") && !strings.Contains(details, "51001") {
		return ""
	}
	return formatUnsupportedOKXInstrumentMessage(rec)
}

func formatUnsupportedOKXInstrumentMessage(rec storage.OrderRecord) string {
	instID := strings.TrimSpace(rec.Result.InstID)
	if instID == "" {
		if derived, _, err := okx.DeriveSwapInstrumentID(rec.Coinpair, rec.Ticker); err == nil {
			instID = derived
		}
	}
	if instID == "" {
		instID = "该币对"
	}
	tradeEnv := "模拟盘"
	if orderRecordTradeEnv(rec) == trading.TradeEnvLive {
		tradeEnv = "实盘"
	}
	return fmt.Sprintf("OKX %s不支持 %s，无法重试", tradeEnv, instID)
}

func isOKXInstrumentNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr okx.APIError
	if errors.As(err, &apiErr) && apiErr.HasCode("51001") {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "51001") && strings.Contains(text, "instrument") && strings.Contains(text, "exist")
}

func ignoredRetrySignalFromRecord(rec storage.OrderRecord, now time.Time) trading.Signal {
	signal := trading.Signal{
		Action:         rec.Action,
		SourceAction:   rec.SourceAction,
		APIID:          rec.APIID,
		TargetExchange: rec.TargetExchange,
		TradeEnv:       orderRecordTradeEnv(rec),
		Coinpair:       rec.Coinpair,
		Ticker:         rec.Ticker,
		Exchange:       rec.SourceExchange,
		SentAt:         now.UTC().Format(time.RFC3339Nano),
		Leverage:       rec.Leverage,
		Risk:           rec.Risk,
		OrderIntent:    rec.OrderIntent,
		PositionEffect: rec.PositionEffect,
		PositionSide:   rec.PositionSide,
		ADX:            rec.ADX,
		MarketStrategy: rec.MarketStrategy,
		RawJSON:        rec.RawJSON,
	}
	if price, err := strconv.ParseFloat(strings.TrimSpace(rec.Price), 64); err == nil && price > 0 {
		signal.Price = trading.NewFlexibleFloat(price)
	}
	if amount, err := strconv.ParseFloat(strings.TrimSpace(rec.Amount), 64); err == nil && amount > 0 {
		signal.Amount = trading.NewFlexibleFloat(amount)
	}
	signal.Normalize()
	return signal
}

func (s *Server) currentRetryMarketPrice(ctx context.Context, cfg config.Config, rec storage.OrderRecord) (float64, error) {
	switch trading.NormalizeExchange(rec.TargetExchange) {
	case trading.ExchangeBinance:
		return s.currentBinanceRetryMarketPrice(ctx, cfg, rec)
	default:
		return s.currentOKXRetryMarketPrice(ctx, cfg, rec)
	}
}

func (s *Server) currentOKXRetryMarketPrice(ctx context.Context, cfg config.Config, rec storage.OrderRecord) (float64, error) {
	instID := strings.TrimSpace(rec.Result.InstID)
	if instID != "" {
		derived, _, err := okx.DeriveSwapInstrumentID(instID, "")
		if err == nil {
			instID = derived
		}
	}
	if instID == "" {
		if sym, ok := cfg.SymbolMeta(rec.Coinpair); ok {
			instID = sym.InstID
		}
	}
	if instID == "" {
		var err error
		instID, _, err = okx.DeriveSwapInstrumentID(rec.Coinpair, rec.Ticker)
		if err != nil {
			return 0, err
		}
	}
	client := okx.Client{
		BaseURL:    cfg.OKXBaseURL(),
		Demo:       cfg.DemoTradingHeaderEnabled(),
		HTTPClient: s.okxHTTPClient(),
	}
	ticker, _, err := client.MarketTicker(ctx, instID)
	if err != nil {
		return 0, fmt.Errorf("refresh OKX retry price for %s: %w", instID, err)
	}
	price, err := tickerMidPrice(ticker)
	if err != nil {
		return 0, err
	}
	return price, nil
}

func (s *Server) currentBinanceRetryMarketPrice(ctx context.Context, cfg config.Config, rec storage.OrderRecord) (float64, error) {
	symbol := strings.TrimSpace(rec.Result.InstID)
	if symbol != "" {
		derived, err := binance.DeriveUSDMSymbol(symbol, rec.Ticker)
		if err == nil {
			symbol = derived
		}
	}
	if symbol == "" {
		var err error
		symbol, err = binance.DeriveUSDMSymbol(rec.Coinpair, rec.Ticker)
		if err != nil {
			return 0, err
		}
	}
	client := binance.Client{
		BaseURL:    cfg.BinanceBaseURL(),
		HTTPClient: s.binanceHTTPClient(),
	}
	ticker, err := client.BookTicker(ctx, symbol)
	if err != nil {
		return 0, fmt.Errorf("refresh Binance retry price for %s: %w", symbol, err)
	}
	price, err := binanceBookMidPrice(ticker)
	if err != nil {
		return 0, err
	}
	return price, nil
}

func retrySignalFromRecord(rec storage.OrderRecord, cfg config.Config, now time.Time, price float64) (trading.Signal, error) {
	if price <= 0 {
		return trading.Signal{}, fmt.Errorf("price %q is invalid: must be positive", trading.NormalizeFloat(price))
	}
	signal := trading.Signal{
		Action:         rec.Action,
		SourceAction:   rec.SourceAction,
		APIID:          rec.APIID,
		TargetExchange: rec.TargetExchange,
		TradeEnv:       orderRecordTradeEnv(rec),
		Coinpair:       rec.Coinpair,
		Ticker:         rec.Ticker,
		Exchange:       rec.SourceExchange,
		Price:          trading.NewFlexibleFloat(price),
		SentAt:         now.UTC().Format(time.RFC3339Nano),
		OrderIntent:    rec.OrderIntent,
		PositionEffect: rec.PositionEffect,
		PositionSide:   rec.PositionSide,
		ADX:            rec.ADX,
		MarketStrategy: rec.MarketStrategy,
		RawJSON:        rec.RawJSON,
	}
	if strings.TrimSpace(rec.Amount) != "" {
		amount, err := parseOrderRecordFloat("amount", rec.Amount)
		if err != nil {
			return trading.Signal{}, err
		}
		signal.Amount = trading.NewFlexibleFloat(amount)
	}
	signal.Normalize()
	if signal.TradeEnv == "" {
		signal.TradeEnv = trading.TradeEnvDemo
	}
	applySignalSourceExchangeRouting(&signal, true)
	execCfg := retryExecutionConfig(cfg, rec)
	execCfg.OrderSettings().ApplyToSignal(&signal)
	if risk, ok := retryRiskFromRecord(rec); ok {
		signal.Risk = risk
	}
	if err := signal.Validate(now, 0, execCfg); err != nil {
		return trading.Signal{}, err
	}
	return signal, nil
}

func retryExecutionConfig(cfg config.Config, rec storage.OrderRecord) config.Config {
	cfg = configForTradeEnv(cfg, orderRecordTradeEnv(rec))
	risk, ok := retryRiskFromRecord(rec)
	if !ok {
		return cfg
	}
	cfg.Trading.RiskType = string(risk.Type)
	switch risk.Type {
	case trading.RiskTPSL:
		if risk.TPPct != nil && risk.TPPct.Set && risk.TPPct.Value > 0 {
			cfg.Trading.TakeProfitPct = risk.TPPct.Value
		}
		if risk.SLPct != nil && risk.SLPct.Set && risk.SLPct.Value > 0 {
			cfg.Trading.StopLossPct = risk.SLPct.Value
		}
	case trading.RiskTrailing:
		if risk.TrailingPct != nil && risk.TrailingPct.Set && risk.TrailingPct.Value > 0 {
			cfg.Trading.TrailingPct = risk.TrailingPct.Value
		}
	}
	cfg.Normalize()
	return cfg
}

func orderRecordTradeEnv(rec storage.OrderRecord) string {
	tradeEnv := trading.NormalizeTradeEnv(rec.TradeEnv)
	if tradeEnv == "" {
		return trading.TradeEnvDemo
	}
	return tradeEnv
}

func retryRiskFromRecord(rec storage.OrderRecord) (trading.Risk, bool) {
	if strings.TrimSpace(string(rec.Risk.Type)) == "" {
		return trading.Risk{}, false
	}
	risk := rec.Risk
	risk.Normalize()
	return risk, true
}

func parseOrderRecordFloat(name, value string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed <= 0 {
		if err == nil {
			err = fmt.Errorf("must be positive")
		}
		return 0, fmt.Errorf("%s %q is invalid: %w", name, value, err)
	}
	return parsed, nil
}

func (s *Server) handleCheckOKX(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only POST is allowed")
		return
	}
	var req struct {
		Exchange string `json:"exchange"`
		APIID    string `json:"api_id"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_json", err.Error())
			return
		}
	}
	exchange := trading.NormalizeExchange(req.Exchange)
	if exchange == trading.ExchangeBinance {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		result, err := s.checkStoredBinance(ctx, s.ConfigStore.Get(), req.APIID)
		if err != nil {
			writeError(w, http.StatusBadGateway, "binance_check_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if exchange == trading.ExchangeGate {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		result, err := s.checkStoredGate(ctx, s.ConfigStore.Get(), req.APIID)
		if err != nil {
			writeError(w, http.StatusBadGateway, "gate_check_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if strings.TrimSpace(req.APIID) != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		result, err := s.checkStoredOKX(ctx, s.ConfigStore.Get(), req.APIID)
		if err != nil {
			writeError(w, http.StatusBadGateway, "okx_check_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if s.Executor == nil {
		writeError(w, http.StatusServiceUnavailable, "not_configured", "executor is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	result, err := s.Executor.Check(ctx, s.ConfigStore.Get())
	if err != nil {
		writeError(w, http.StatusBadGateway, "okx_check_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) checkStoredOKX(ctx context.Context, cfg trading.RuntimeConfig, apiID string) (map[string]any, error) {
	store := s.okxCredentialStore(credentialEnvironmentFromRuntimeConfig(cfg))
	if store == nil {
		return nil, errors.New("OKX credential store is not configured")
	}
	creds, resolvedID, err := store.OKXCredentials(apiID)
	if err != nil {
		return nil, err
	}
	return checkOKXCredentials(ctx, cfg, resolvedID, creds)
}

func checkOKXCredentials(ctx context.Context, cfg trading.RuntimeConfig, apiID string, creds okx.Credentials) (map[string]any, error) {
	trader := okx.Trader{
		Credentials: creds,
		HTTPClient:  &http.Client{Timeout: 15 * time.Second},
	}
	return trader.CheckCredentials(ctx, cfg, apiID, creds)
}

func (s *Server) checkStoredBinance(ctx context.Context, cfg trading.RuntimeConfig, apiID string) (map[string]any, error) {
	store := s.binanceCredentialStore(credentialEnvironmentFromRuntimeConfig(cfg))
	if store == nil {
		return nil, errors.New("Binance credential store is not configured")
	}
	creds, resolvedID, err := store.BinanceCredentials(apiID)
	if err != nil {
		return nil, err
	}
	return checkBinanceCredentials(ctx, cfg, resolvedID, creds)
}

func (s *Server) checkStoredGate(ctx context.Context, cfg trading.RuntimeConfig, apiID string) (map[string]any, error) {
	store := s.gateCredentialStore(credentialEnvironmentFromRuntimeConfig(cfg))
	if store == nil {
		return nil, errors.New("Gate credential store is not configured")
	}
	creds, resolvedID, err := store.GateCredentials(apiID)
	if err != nil {
		return nil, err
	}
	return checkGateCredentials(ctx, cfg, resolvedID, creds)
}

func checkGateCredentials(ctx context.Context, cfg trading.RuntimeConfig, apiID string, creds gate.Credentials) (map[string]any, error) {
	trader := gate.Trader{Credentials: creds, HTTPClient: &http.Client{Timeout: 15 * time.Second}}
	return trader.CheckCredentials(ctx, cfg, apiID, creds)
}

func checkBinanceCredentials(ctx context.Context, cfg trading.RuntimeConfig, apiID string, creds binance.Credentials) (map[string]any, error) {
	trader := binance.Trader{
		Credentials: creds,
		HTTPClient:  &http.Client{Timeout: 15 * time.Second},
	}
	return trader.CheckCredentials(ctx, cfg, apiID, creds)
}

func credentialFieldsProvided(creds okx.Credentials) bool {
	return strings.TrimSpace(creds.APIKey) != "" ||
		strings.TrimSpace(creds.SecretKey) != "" ||
		strings.TrimSpace(creds.Passphrase) != ""
}

func mergeMissingCredentials(creds *okx.Credentials, stored okx.Credentials) {
	if strings.TrimSpace(creds.APIKey) == "" {
		creds.APIKey = stored.APIKey
	}
	if strings.TrimSpace(creds.SecretKey) == "" {
		creds.SecretKey = stored.SecretKey
	}
	if strings.TrimSpace(creds.Passphrase) == "" {
		creds.Passphrase = stored.Passphrase
	}
}

func binanceCredentialFieldsProvided(creds binance.Credentials) bool {
	return strings.TrimSpace(creds.APIKey) != "" ||
		strings.TrimSpace(creds.SecretKey) != ""
}

func mergeMissingBinanceCredentials(creds *binance.Credentials, stored binance.Credentials) {
	if strings.TrimSpace(creds.APIKey) == "" {
		creds.APIKey = stored.APIKey
	}
	if strings.TrimSpace(creds.SecretKey) == "" {
		creds.SecretKey = stored.SecretKey
	}
}

func gateCredentialFieldsProvided(creds gate.Credentials) bool {
	return strings.TrimSpace(creds.APIKey) != "" || strings.TrimSpace(creds.SecretKey) != ""
}
func mergeMissingGateCredentials(creds *gate.Credentials, stored gate.Credentials) {
	if strings.TrimSpace(creds.APIKey) == "" {
		creds.APIKey = stored.APIKey
	}
	if strings.TrimSpace(creds.SecretKey) == "" {
		creds.SecretKey = stored.SecretKey
	}
}

func (s *Server) handleTVBotLogin(w http.ResponseWriter, r *http.Request) {
	next := sanitizeAdminNext(r.URL.Query().Get("next"))
	switch r.Method {
	case http.MethodGet:
		if s.authorized(r) {
			http.Redirect(w, r, next, http.StatusFound)
			return
		}
		writeHTML(w, http.StatusOK, renderTVBotLoginHTML("", next, s.BuildInfo))
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err != nil {
			writeHTML(w, http.StatusBadRequest, renderTVBotLoginHTML("登录请求无效", next, s.BuildInfo))
			return
		}
		next = sanitizeAdminNext(r.FormValue("next"))
		if !s.validAdminPassword(r.FormValue("username"), r.FormValue("password")) {
			writeHTML(w, http.StatusUnauthorized, renderTVBotLoginHTML("用户名或密码错误", next, s.BuildInfo))
			return
		}
		s.setAdminSessionCookie(w, r)
		http.Redirect(w, r, next, http.StatusSeeOther)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET and POST are allowed")
	}
}

func (s *Server) handleTVBotLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET and POST are allowed")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
	})
	http.Redirect(w, r, "/tvbot/login", http.StatusSeeOther)
}

func (s *Server) validAdminPassword(user, pass string) bool {
	user = strings.TrimSpace(user)
	if user == "" || s.AdminUser == "" || s.AdminPass == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(user), []byte(s.AdminUser)) == 1 &&
		subtle.ConstantTimeCompare([]byte(pass), []byte(s.AdminPass)) == 1
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.authorized(r) {
		return true
	}
	if requestWantsHTML(r) {
		next := sanitizeAdminNext(r.URL.RequestURI())
		http.Redirect(w, r, "/tvbot/login?next="+url.QueryEscape(next), http.StatusFound)
		return false
	}
	writeError(w, http.StatusUnauthorized, "unauthorized", "admin credentials are required")
	return false
}

func (s *Server) authorized(r *http.Request) bool {
	got := r.Header.Get("X-Admin-Token")
	if s.AdminToken != "" && got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.AdminToken)) == 1 {
		return true
	}
	if s.validAdminSessionCookie(r) {
		return true
	}
	user, pass, ok := r.BasicAuth()
	if !ok || s.AdminUser == "" || s.AdminPass == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(user), []byte(s.AdminUser)) == 1 &&
		subtle.ConstantTimeCompare([]byte(pass), []byte(s.AdminPass)) == 1
}

func (s *Server) setAdminSessionCookie(w http.ResponseWriter, r *http.Request) {
	expires := s.now().Add(adminSessionTTL)
	payload := s.AdminUser + "|" + strconv.FormatInt(expires.Unix(), 10)
	encodedPayload := base64.RawURLEncoding.EncodeToString([]byte(payload))
	token := encodedPayload + "." + s.signAdminSession(encodedPayload)
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(adminSessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
	})
}

func (s *Server) validAdminSessionCookie(r *http.Request) bool {
	cookie, err := r.Cookie(adminSessionCookieName)
	if err != nil {
		return false
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	wantSig := s.signAdminSession(parts[0])
	if wantSig == "" || !hmac.Equal([]byte(parts[1]), []byte(wantSig)) {
		return false
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	payloadParts := strings.Split(string(payloadBytes), "|")
	if len(payloadParts) != 2 || payloadParts[0] != s.AdminUser {
		return false
	}
	expiresUnix, err := strconv.ParseInt(payloadParts[1], 10, 64)
	if err != nil {
		return false
	}
	return s.now().Unix() <= expiresUnix
}

func (s *Server) signAdminSession(encodedPayload string) string {
	secret := s.adminSessionSecret()
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(encodedPayload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Server) adminSessionSecret() string {
	if strings.TrimSpace(s.AdminToken) != "" {
		return s.AdminToken
	}
	if s.AdminUser == "" || s.AdminPass == "" {
		return ""
	}
	return s.AdminUser + "|" + s.AdminPass
}

func requestWantsHTML(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/html") ||
		r.Header.Get("Sec-Fetch-Dest") == "document" ||
		r.URL.Path == "/tvbot" ||
		r.URL.Path == "/tvbot/"
}

func sanitizeAdminNext(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "/tvbot/"
	}
	if strings.HasPrefix(raw, "/tvbot") || strings.HasPrefix(raw, "/upgrade") {
		return raw
	}
	return "/tvbot/"
}

func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

type configPatch struct {
	Server       *serverPatch  `json:"server"`
	DataFile     *string       `json:"data_file"`
	DatabaseFile *string       `json:"database_file"`
	Trading      *tradingPatch `json:"trading"`
	UI           *uiPatch      `json:"ui"`
}

type serverPatch struct {
	Addr *string `json:"addr"`
}

type tradingPatch struct {
	Env                       *string                       `json:"env"`
	AllowLiveTrading          *bool                         `json:"allow_live_trading"`
	BaseURL                   *string                       `json:"base_url"`
	BinanceBaseURL            *string                       `json:"binance_base_url"`
	BinanceDemoBaseURL        *string                       `json:"binance_demo_base_url"`
	DefaultMarginMode         *string                       `json:"default_margin_mode"`
	PositionMode              *string                       `json:"position_mode"`
	MarketTurnoverScope       *string                       `json:"market_turnover_scope"`
	SignalTTLSeconds          *int                          `json:"signal_ttl_seconds"`
	IgnoredCoinpair           *string                       `json:"ignored_coinpair"`
	IgnoredCoinpairs          *[]string                     `json:"ignored_coinpairs"`
	CoinpairWhitelist         *[]string                     `json:"coinpair_whitelist"`
	OrderAmountUSDT           *float64                      `json:"order_amount_usdt"`
	Leverage                  *int                          `json:"leverage"`
	OrderType                 *string                       `json:"order_type"`
	RiskType                  *string                       `json:"risk_type"`
	TakeProfitPct             *float64                      `json:"take_profit_pct"`
	StopLossPct               *float64                      `json:"stop_loss_pct"`
	TrailingPct               *float64                      `json:"trailing_pct"`
	LongLimitPriceMultiplier  *float64                      `json:"long_limit_price_multiplier"`
	ShortLimitPriceMultiplier *float64                      `json:"short_limit_price_multiplier"`
	LayeredOrder              *trading.LayeredOrderSettings `json:"layered_order"`
	LossCooldown              *lossCooldownPatch            `json:"loss_cooldown"`
	FillMonitor               *config.FillMonitorConfig     `json:"fill_monitor"`
	AutoReentry               *config.AutoReentryConfig     `json:"auto_reentry"`
	PositionMonitor           *config.PositionMonitorConfig `json:"position_monitor"`
}

type lossCooldownPatch struct {
	Enabled       *bool `json:"enabled"`
	GlobalEnabled *bool `json:"global_enabled"`
	CoinpairHours *int  `json:"coinpair_hours"`
	GlobalHours   *int  `json:"global_hours"`
	Hours         *int  `json:"hours"`
}

type uiPatch struct {
	DefaultTab   *string                    `json:"default_tab"`
	MenuItems    *[]config.MenuItemConfig   `json:"menu_items"`
	TableColumns *config.TableColumnsConfig `json:"table_columns"`
}

func applyConfigPatch(c *config.Config, patch configPatch) {
	if patch.Server != nil && patch.Server.Addr != nil {
		c.Server.Addr = *patch.Server.Addr
	}
	if patch.DataFile != nil {
		c.DataFile = *patch.DataFile
	}
	if patch.DatabaseFile != nil {
		c.DatabaseFile = *patch.DatabaseFile
	}
	if patch.UI != nil {
		if patch.UI.DefaultTab != nil {
			c.UI.DefaultTab = *patch.UI.DefaultTab
		}
		if patch.UI.MenuItems != nil {
			c.UI.MenuItems = *patch.UI.MenuItems
		}
		if patch.UI.TableColumns != nil {
			c.UI.TableColumns = *patch.UI.TableColumns
		}
	}
	if patch.Trading == nil {
		return
	}
	if patch.Trading.Env != nil {
		c.Trading.Env = *patch.Trading.Env
	}
	if patch.Trading.AllowLiveTrading != nil {
		c.Trading.AllowLiveTrading = *patch.Trading.AllowLiveTrading
	}
	if patch.Trading.BaseURL != nil {
		c.Trading.BaseURL = *patch.Trading.BaseURL
	}
	if patch.Trading.BinanceBaseURL != nil {
		c.Trading.BinanceBaseURL = *patch.Trading.BinanceBaseURL
	}
	if patch.Trading.BinanceDemoBaseURL != nil {
		c.Trading.BinanceDemoBaseURL = *patch.Trading.BinanceDemoBaseURL
	}
	if patch.Trading.DefaultMarginMode != nil {
		c.Trading.DefaultMarginMode = *patch.Trading.DefaultMarginMode
	}
	if patch.Trading.PositionMode != nil {
		c.Trading.PositionMode = *patch.Trading.PositionMode
	}
	if patch.Trading.MarketTurnoverScope != nil {
		c.Trading.MarketTurnoverScope = *patch.Trading.MarketTurnoverScope
	}
	if patch.Trading.SignalTTLSeconds != nil {
		c.Trading.SignalTTLSeconds = *patch.Trading.SignalTTLSeconds
	}
	if patch.Trading.IgnoredCoinpairs != nil {
		c.Trading.IgnoredCoinpairs = append([]string(nil), (*patch.Trading.IgnoredCoinpairs)...)
		c.Trading.IgnoredCoinpair = ""
	} else if patch.Trading.IgnoredCoinpair != nil {
		c.Trading.IgnoredCoinpairs = nil
		c.Trading.IgnoredCoinpair = *patch.Trading.IgnoredCoinpair
	}
	if patch.Trading.CoinpairWhitelist != nil {
		c.Trading.CoinpairWhitelist = append([]string(nil), (*patch.Trading.CoinpairWhitelist)...)
	}
	if patch.Trading.OrderAmountUSDT != nil {
		c.Trading.OrderAmountUSDT = *patch.Trading.OrderAmountUSDT
	}
	if patch.Trading.Leverage != nil {
		c.Trading.Leverage = *patch.Trading.Leverage
	}
	if patch.Trading.OrderType != nil {
		c.Trading.OrderType = *patch.Trading.OrderType
	}
	if patch.Trading.RiskType != nil {
		c.Trading.RiskType = *patch.Trading.RiskType
	}
	if patch.Trading.TakeProfitPct != nil {
		c.Trading.TakeProfitPct = *patch.Trading.TakeProfitPct
	}
	if patch.Trading.StopLossPct != nil {
		c.Trading.StopLossPct = *patch.Trading.StopLossPct
	}
	if patch.Trading.TrailingPct != nil {
		c.Trading.TrailingPct = *patch.Trading.TrailingPct
	}
	if patch.Trading.LongLimitPriceMultiplier != nil {
		c.Trading.LongLimitPriceMultiplier = *patch.Trading.LongLimitPriceMultiplier
	}
	if patch.Trading.ShortLimitPriceMultiplier != nil {
		c.Trading.ShortLimitPriceMultiplier = *patch.Trading.ShortLimitPriceMultiplier
	}
	if patch.Trading.LayeredOrder != nil {
		c.Trading.LayeredOrder = *patch.Trading.LayeredOrder
	}
	if patch.Trading.LossCooldown != nil {
		if patch.Trading.LossCooldown.Enabled != nil {
			c.Trading.LossCooldown.Enabled = *patch.Trading.LossCooldown.Enabled
		}
		if patch.Trading.LossCooldown.GlobalEnabled != nil {
			c.Trading.LossCooldown.GlobalEnabled = *patch.Trading.LossCooldown.GlobalEnabled
		}
		if patch.Trading.LossCooldown.CoinpairHours != nil {
			c.Trading.LossCooldown.CoinpairHours = *patch.Trading.LossCooldown.CoinpairHours
		}
		if patch.Trading.LossCooldown.GlobalHours != nil {
			c.Trading.LossCooldown.GlobalHours = *patch.Trading.LossCooldown.GlobalHours
		}
		if patch.Trading.LossCooldown.Hours != nil {
			// Keep older API clients functional by applying the legacy shared
			// value to both scopes when no new value was supplied for that scope.
			if patch.Trading.LossCooldown.CoinpairHours == nil {
				c.Trading.LossCooldown.CoinpairHours = *patch.Trading.LossCooldown.Hours
			}
			if patch.Trading.LossCooldown.GlobalHours == nil {
				c.Trading.LossCooldown.GlobalHours = *patch.Trading.LossCooldown.Hours
			}
		}
	}
	if patch.Trading.FillMonitor != nil {
		c.Trading.FillMonitor = *patch.Trading.FillMonitor
	}
	if patch.Trading.AutoReentry != nil {
		c.Trading.AutoReentry = *patch.Trading.AutoReentry
	}
	if patch.Trading.PositionMonitor != nil {
		c.Trading.PositionMonitor = *patch.Trading.PositionMonitor
	}
}

func decodeSymbols(r *http.Request) (map[string]config.SymbolConfig, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	raw := json.RawMessage(body)
	var wrapped struct {
		Symbols map[string]config.SymbolConfig `json:"symbols"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Symbols != nil {
		return wrapped.Symbols, nil
	}
	var direct map[string]config.SymbolConfig
	if err := json.Unmarshal(raw, &direct); err != nil {
		return nil, fmt.Errorf("expected symbol map or {\"symbols\":...}: %w", err)
	}
	return direct, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error":   code,
		"message": message,
	})
}
