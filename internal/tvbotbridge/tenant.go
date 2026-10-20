package tvbotbridge

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotserver "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotserver"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
)

// tenantManager keeps the user-owned TVBot configuration and order database
// together. Market data remains shared by the liquidation service, while every
// account's trading settings, fills, positions and analysis use this manager's
// private SQLite file.
type tenantManager struct {
	rootPath    string
	legacyOwner string
	rootConfig  *tvbotconfig.Store
	rootOrders  *tvbotstorage.OrderStore
	credentials *userCredentialResolver
	baseConfig  tvbotconfig.Config

	mu        sync.Mutex
	resources map[string]*tenantResource
	started   map[string]bool
	start     func(tvbotserver.UserScope)
}

type tenantResource struct {
	tenantID string
	username string
	config   *tvbotconfig.Store
	orders   *tvbotstorage.OrderStore
}

func newTenantManager(rootPath, legacyOwner string, rootConfig *tvbotconfig.Store, rootOrders *tvbotstorage.OrderStore, credentials *userCredentialResolver) *tenantManager {
	base := tvbotconfig.Default()
	if rootConfig != nil {
		base = rootConfig.Get()
	}
	return &tenantManager{
		rootPath:    strings.TrimSpace(rootPath),
		legacyOwner: strings.TrimSpace(legacyOwner),
		rootConfig:  rootConfig,
		rootOrders:  rootOrders,
		credentials: credentials,
		baseConfig:  base,
		resources:   make(map[string]*tenantResource),
		started:     make(map[string]bool),
	}
}

func (m *tenantManager) ForUser(username string) (tvbotserver.UserCredentials, error) {
	if m == nil || m.credentials == nil {
		return tvbotserver.UserCredentials{}, errors.New("tenant credential resolver is unavailable")
	}
	return m.credentials.ForUser(username)
}

func (m *tenantManager) ForUserScope(username string) (tvbotserver.UserScope, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return tvbotserver.UserScope{}, errors.New("username is required for tenant scope")
	}
	credentials, err := m.ForUser(username)
	if err != nil {
		return tvbotserver.UserScope{}, err
	}
	if strings.EqualFold(username, m.legacyOwner) {
		return tvbotserver.UserScope{TenantID: tenantIDForUser(username), ConfigStore: m.rootConfig, Orders: m.rootOrders, Credentials: credentials}, nil
	}
	resource, err := m.resourceForUser(username)
	if err != nil {
		return tvbotserver.UserScope{}, err
	}
	scope := tvbotserver.UserScope{TenantID: resource.tenantID, ConfigStore: resource.config, Orders: resource.orders, Credentials: credentials}
	m.startScope(username, scope)
	return scope, nil
}

func (m *tenantManager) SetStart(fn func(tvbotserver.UserScope)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.start = fn
	m.mu.Unlock()
}

func (m *tenantManager) startScope(username string, scope tvbotserver.UserScope) {
	m.mu.Lock()
	if m.started[username] || m.start == nil {
		m.mu.Unlock()
		return
	}
	m.started[username] = true
	start := m.start
	m.mu.Unlock()
	start(scope)
}

func (m *tenantManager) ForTenantID(tenantID string) (tvbotserver.UserScope, error) {
	username, err := userForTenantID(tenantID)
	if err != nil {
		return tvbotserver.UserScope{}, err
	}
	return m.ForUserScope(username)
}

func (m *tenantManager) DefaultTenantID() string {
	owner := m.legacyOwner
	if owner == "" {
		owner = "admin"
	}
	return tenantIDForUser(owner)
}

func (m *tenantManager) resourceForUser(username string) (*tenantResource, error) {
	tenantID := tenantIDForUser(username)
	m.mu.Lock()
	defer m.mu.Unlock()
	if resource, ok := m.resources[username]; ok {
		return resource, nil
	}
	if strings.TrimSpace(m.rootPath) == "" {
		return nil, errors.New("tenant data path is not configured")
	}
	dir := filepath.Join(m.rootPath, userDirectoryName(username))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	configPath := filepath.Join(dir, "tvbot-config.json")
	databasePath := filepath.Join(dir, "tvbot.db")
	cfg := m.baseConfig
	if loaded, err := tvbotconfig.Load(configPath); err == nil {
		cfg = loaded
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load tenant config for %s: %w", username, err)
	}
	cfg.DatabaseFile = databasePath
	cfg.DataFile = filepath.Join(dir, "orders.json")
	configStore := tvbotconfig.NewStore(configPath, cfg)
	orders, err := tvbotstorage.NewSQLiteOrderStore(databasePath, "")
	if err != nil {
		return nil, fmt.Errorf("open tenant order store for %s: %w", username, err)
	}
	resource := &tenantResource{tenantID: tenantID, username: username, config: configStore, orders: orders}
	m.resources[username] = resource
	return resource, nil
}

func (m *tenantManager) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var first error
	for username, resource := range m.resources {
		if resource == nil || resource.orders == nil {
			continue
		}
		if err := resource.orders.Close(); err != nil && first == nil {
			first = fmt.Errorf("close tenant order store for %s: %w", username, err)
		}
	}
	return first
}

func tenantIDForUser(username string) string {
	return "u_" + base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(username)))
}

func userForTenantID(tenantID string) (string, error) {
	tenantID = strings.TrimSpace(tenantID)
	if !strings.HasPrefix(tenantID, "u_") {
		return "", errors.New("tenant_id has an invalid format")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(tenantID, "u_"))
	if err != nil || strings.TrimSpace(string(decoded)) == "" {
		return "", errors.New("tenant_id has an invalid format")
	}
	return strings.TrimSpace(string(decoded)), nil
}
