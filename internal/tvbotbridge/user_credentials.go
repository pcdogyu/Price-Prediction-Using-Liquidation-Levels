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
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
	tvbotserver "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotserver"
)

type userCredentialResolver struct {
	root              string
	legacyOwner       string
	legacyOKXPath     string
	legacyBinancePath string
	initialOKX        tvbotokx.Credentials
	initialBinance    tvbotbinance.Credentials

	mu    sync.Mutex
	users map[string]tvbotserver.UserCredentials
}

func newUserCredentialResolver(root, legacyOwner, legacyOKXPath, legacyBinancePath string, initialOKX tvbotokx.Credentials, initialBinance tvbotbinance.Credentials) (*userCredentialResolver, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("user credential root is required")
	}
	return &userCredentialResolver{
		root:              root,
		legacyOwner:       strings.TrimSpace(legacyOwner),
		legacyOKXPath:     strings.TrimSpace(legacyOKXPath),
		legacyBinancePath: strings.TrimSpace(legacyBinancePath),
		initialOKX:        initialOKX,
		initialBinance:    initialBinance,
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
	okxPath := filepath.Join(userDir, "okx-credentials.json")
	binancePath := filepath.Join(userDir, "binance-credentials.json")
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		return tvbotserver.UserCredentials{}, err
	}
	isLegacyOwner := r.legacyOwner != "" && username == r.legacyOwner
	if isLegacyOwner {
		if err := copyLegacyCredential(r.legacyOKXPath, okxPath); err != nil {
			return tvbotserver.UserCredentials{}, err
		}
		if err := copyLegacyCredential(r.legacyBinancePath, binancePath); err != nil {
			return tvbotserver.UserCredentials{}, err
		}
	}

	okxCredentials, err := tvbotokx.NewCredentialStore(okxPath, tvbotokx.Credentials{})
	if err != nil {
		return tvbotserver.UserCredentials{}, err
	}
	binanceCredentials, err := tvbotbinance.NewCredentialStore(binancePath, tvbotbinance.Credentials{})
	if err != nil {
		return tvbotserver.UserCredentials{}, err
	}
	if isLegacyOwner {
		if err := seedInitialOKX(okxCredentials, r.initialOKX); err != nil {
			return tvbotserver.UserCredentials{}, err
		}
		if err := seedInitialBinance(binanceCredentials, r.initialBinance); err != nil {
			return tvbotserver.UserCredentials{}, err
		}
	}
	credentials := tvbotserver.UserCredentials{OKX: okxCredentials, Binance: binanceCredentials}
	r.users[username] = credentials
	return credentials, nil
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
