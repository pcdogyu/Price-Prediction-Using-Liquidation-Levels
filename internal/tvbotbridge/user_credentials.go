package tvbotbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotgate "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotgate"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbotserver "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotserver"
)

type userCredentialResolver struct {
	root              string
	legacyOwner       string
	legacyOKXPath     string
	legacyBinancePath string
	legacyGatePath    string
	legacyEnv         string
	initialOKX        tvbotokx.Credentials
	initialBinance    tvbotbinance.Credentials
	initialGate       tvbotgate.Credentials

	mu    sync.Mutex
	users map[string]tvbotserver.UserCredentials
}

func newUserCredentialResolver(root, legacyOwner, legacyOKXPath, legacyBinancePath, legacyGatePath string, initialOKX tvbotokx.Credentials, initialBinance tvbotbinance.Credentials, initialGate tvbotgate.Credentials, defaultEnv ...string) (*userCredentialResolver, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("user credential root is required")
	}
	legacyEnv := "demo"
	if len(defaultEnv) > 0 && strings.EqualFold(strings.TrimSpace(defaultEnv[0]), "live") {
		legacyEnv = "live"
	}
	return &userCredentialResolver{
		root:              root,
		legacyOwner:       strings.TrimSpace(legacyOwner),
		legacyOKXPath:     strings.TrimSpace(legacyOKXPath),
		legacyBinancePath: strings.TrimSpace(legacyBinancePath),
		legacyGatePath:    strings.TrimSpace(legacyGatePath),
		legacyEnv:         legacyEnv,
		initialOKX:        initialOKX,
		initialBinance:    initialBinance,
		initialGate:       initialGate,
		users:             make(map[string]tvbotserver.UserCredentials),
	}, nil
}

func (r *userCredentialResolver) ForUser(username string) (tvbotserver.UserCredentials, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return tvbotserver.UserCredentials{}, errors.New("username is required for user credentials")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if credentials, ok := r.users[username]; ok {
		return credentials, nil
	}

	userDir := filepath.Join(r.root, "users", userDirectoryName(username))
	legacyOKXPath := filepath.Join(userDir, "okx-credentials.json")
	legacyBinancePath := filepath.Join(userDir, "binance-credentials.json")
	legacyGatePath := filepath.Join(userDir, "gate-credentials.json")
	okxDemoPath := filepath.Join(userDir, "okx-credentials-demo.json")
	okxLivePath := filepath.Join(userDir, "okx-credentials-live.json")
	binanceDemoPath := filepath.Join(userDir, "binance-credentials-demo.json")
	binanceLivePath := filepath.Join(userDir, "binance-credentials-live.json")
	gateDemoPath := filepath.Join(userDir, "gate-credentials-demo.json")
	gateLivePath := filepath.Join(userDir, "gate-credentials-live.json")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		return tvbotserver.UserCredentials{}, err
	}
	isLegacyOwner := r.legacyOwner != "" && username == r.legacyOwner
	legacyEnvPath := func(demo, live string) string {
		if r.legacyEnv == "live" {
			return live
		}
		return demo
	}
	if isLegacyOwner {
		if err := copyLegacyCredential(r.legacyOKXPath, legacyEnvPath(okxDemoPath, okxLivePath)); err != nil {
			return tvbotserver.UserCredentials{}, err
		}
		if err := copyLegacyCredential(r.legacyBinancePath, legacyEnvPath(binanceDemoPath, binanceLivePath)); err != nil {
			return tvbotserver.UserCredentials{}, err
		}
		if err := copyLegacyCredential(r.legacyGatePath, legacyEnvPath(gateDemoPath, gateLivePath)); err != nil {
			return tvbotserver.UserCredentials{}, err
		}
	} else {
		if err := copyLegacyCredential(legacyOKXPath, legacyEnvPath(okxDemoPath, okxLivePath)); err != nil {
			return tvbotserver.UserCredentials{}, err
		}
		if err := copyLegacyCredential(legacyBinancePath, legacyEnvPath(binanceDemoPath, binanceLivePath)); err != nil {
			return tvbotserver.UserCredentials{}, err
		}
		if err := copyLegacyCredential(legacyGatePath, legacyEnvPath(gateDemoPath, gateLivePath)); err != nil {
			return tvbotserver.UserCredentials{}, err
		}
	}

	okxCredentials, err := tvbotokx.NewCredentialStore(okxDemoPath, tvbotokx.Credentials{})
	if err != nil {
		return tvbotserver.UserCredentials{}, err
	}
	okxLiveCredentials, err := tvbotokx.NewCredentialStore(okxLivePath, tvbotokx.Credentials{})
	if err != nil {
		return tvbotserver.UserCredentials{}, err
	}
	binanceCredentials, err := tvbotbinance.NewCredentialStore(binanceDemoPath, tvbotbinance.Credentials{})
	if err != nil {
		return tvbotserver.UserCredentials{}, err
	}
	binanceLiveCredentials, err := tvbotbinance.NewCredentialStore(binanceLivePath, tvbotbinance.Credentials{})
	if err != nil {
		return tvbotserver.UserCredentials{}, err
	}
	gateCredentials, err := tvbotgate.NewCredentialStore(gateDemoPath, tvbotgate.Credentials{})
	if err != nil {
		return tvbotserver.UserCredentials{}, err
	}
	gateLiveCredentials, err := tvbotgate.NewCredentialStore(gateLivePath, tvbotgate.Credentials{})
	if err != nil {
		return tvbotserver.UserCredentials{}, err
	}
	if isLegacyOwner {
		okxInitialStore := okxCredentials
		binanceInitialStore := binanceCredentials
		gateInitialStore := gateCredentials
		if r.legacyEnv == "live" {
			okxInitialStore = okxLiveCredentials
			binanceInitialStore = binanceLiveCredentials
			gateInitialStore = gateLiveCredentials
		}
		if err := seedInitialOKX(okxInitialStore, r.initialOKX); err != nil {
			return tvbotserver.UserCredentials{}, err
		}
		if err := seedInitialBinance(binanceInitialStore, r.initialBinance); err != nil {
			return tvbotserver.UserCredentials{}, err
		}
		if err := seedInitialGate(gateInitialStore, r.initialGate); err != nil {
			return tvbotserver.UserCredentials{}, err
		}
	}
	credentials := tvbotserver.UserCredentials{OKX: okxCredentials, OKXLive: okxLiveCredentials, Binance: binanceCredentials, BinanceLive: binanceLiveCredentials, Gate: gateCredentials, GateLive: gateLiveCredentials}
	r.users[username] = credentials
	return credentials, nil
}

func seedInitialGate(store *tvbotgate.CredentialStore, initial tvbotgate.Credentials) error {
	if store.Status().Configured || strings.TrimSpace(initial.APIKey) == "" || strings.TrimSpace(initial.SecretKey) == "" {
		return nil
	}
	_, err := store.UpdateAccount(tvbotgate.CredentialAccountUpdate{ID: tvbotgate.DefaultCredentialID, Active: true, Credentials: initial})
	return err
}

func seedInitialOKX(store *tvbotokx.CredentialStore, initial tvbotokx.Credentials) error {
	if store.Status().Configured || strings.TrimSpace(initial.APIKey) == "" || strings.TrimSpace(initial.SecretKey) == "" || strings.TrimSpace(initial.Passphrase) == "" {
		return nil
	}
	_, err := store.UpdateAccount(tvbotokx.CredentialAccountUpdate{ID: tvbotokx.DefaultCredentialID, Active: true, Credentials: initial})
	return err
}

func seedInitialBinance(store *tvbotbinance.CredentialStore, initial tvbotbinance.Credentials) error {
	if store.Status().Configured || strings.TrimSpace(initial.APIKey) == "" || strings.TrimSpace(initial.SecretKey) == "" {
		return nil
	}
	_, err := store.UpdateAccount(tvbotbinance.CredentialAccountUpdate{ID: tvbotbinance.DefaultCredentialID, Active: true, Credentials: initial})
	return err
}

func userDirectoryName(username string) string {
	digest := sha256.Sum256([]byte(username))
	return hex.EncodeToString(digest[:])
}
