package tvbotbridge

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	appstore "github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotserver "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotserver"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
	tvbottrading "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbottrading"
)

// tenantManager keeps the user-owned TVBot configuration and order database
// together. Market data remains shared by the liquidation service, while every
// account's trading settings, fills, positions and analysis use this manager's
// private SQLite file.
type tenantManager struct {
	rootPath      string
	legacyOwner   string
	rootConfig    *tvbotconfig.Store
	rootOrders    *tvbotstorage.OrderStore
	authStore     *appstore.Store
	credentials   *userCredentialResolver
	baseConfig    tvbotconfig.Config
	defaultConfig tvbotconfig.Config

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

func newTenantManager(rootPath, legacyOwner string, rootConfig *tvbotconfig.Store, rootOrders *tvbotstorage.OrderStore, credentials *userCredentialResolver, authStores ...*appstore.Store) *tenantManager {
	defaultConfig := tvbotconfig.Default()
	base := defaultConfig
	if rootConfig != nil {
		base = rootConfig.Get()
	}
	var authStore *appstore.Store
	if len(authStores) > 0 {
		authStore = authStores[0]
	}
	return &tenantManager{
		rootPath:      strings.TrimSpace(rootPath),
		legacyOwner:   strings.TrimSpace(legacyOwner),
		rootConfig:    rootConfig,
		rootOrders:    rootOrders,
		authStore:     authStore,
		credentials:   credentials,
		baseConfig:    base,
		defaultConfig: defaultConfig,
		resources:     make(map[string]*tenantResource),
		started:       make(map[string]bool),
	}
}

type tenantConfigRecord struct {
	Username string
	TenantID string
	Config   tvbotconfig.Config
	Exists   bool
}

// readOnlyTenantConfigs enumerates active accounts without calling
// resourceForUser. In particular, a missing tenant config does not create a
// directory or SQLite database just because the daily candidate refresh ran.
func (m *tenantManager) readOnlyTenantConfigs(ctx context.Context) ([]tenantConfigRecord, error) {
	if m == nil {
		return nil, errors.New("tenant manager is unavailable")
	}
	usernames := []string{}
	if m.authStore != nil {
		records, err := m.authStore.LoadAuthUserRecords(ctx)
		if err != nil {
			return nil, err
		}
		for username, record := range records {
			if !record.Disabled && strings.TrimSpace(username) != "" {
				usernames = append(usernames, username)
			}
		}
	}
	if owner := strings.TrimSpace(m.legacyOwner); owner != "" {
		found := false
		for _, username := range usernames {
			if strings.EqualFold(username, owner) {
				found = true
				break
			}
		}
		if !found {
			usernames = append(usernames, owner)
		}
	}
	sort.Strings(usernames)
	seen := make(map[string]struct{}, len(usernames))
	rows := make([]tenantConfigRecord, 0, len(usernames))
	for _, username := range usernames {
		key := strings.ToLower(strings.TrimSpace(username))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if strings.EqualFold(username, m.legacyOwner) {
			cfg := m.baseConfig
			if m.rootConfig != nil {
				cfg = m.rootConfig.Get()
			}
			rows = append(rows, tenantConfigRecord{Username: username, TenantID: tenantIDForUser(username), Config: cfg, Exists: true})
			continue
		}
		path := filepath.Join(m.rootPath, userDirectoryName(username), "tvbot-config.json")
		cfg, err := tvbotconfig.Load(path)
		if errors.Is(err, os.ErrNotExist) {
			rows = append(rows, tenantConfigRecord{Username: username, TenantID: tenantIDForUser(username), Config: m.defaultConfig, Exists: false})
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("load tenant config for %s: %w", username, err)
		}
		rows = append(rows, tenantConfigRecord{Username: username, TenantID: tenantIDForUser(username), Config: cfg, Exists: true})
	}
	return rows, nil
}

func (m *tenantManager) readOnlyScope(username string, cfg tvbotconfig.Config) (tvbotserver.UserScope, error) {
	if m == nil || m.credentials == nil {
		return tvbotserver.UserScope{}, errors.New("tenant credential resolver is unavailable")
	}
	credentials, err := m.credentials.ReadOnlyForUser(username)
	if err != nil {
		return tvbotserver.UserScope{}, err
	}
	return tvbotserver.UserScope{TenantID: tenantIDForUser(username), ConfigStore: tvbotconfig.NewStore("", cfg), Credentials: credentials}, nil
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

// TradeTargetForUser returns the destination selected by the user in API-key
// settings. An empty result deliberately means “use the legacy process-wide
// webhook target”, preserving old installations until a user selects an API.
func (m *tenantManager) TradeTargetForUser(username string) (string, string, string, bool) {
	scope, err := m.ForUserScope(username)
	if err != nil || scope.ConfigStore == nil {
		return "", "", "", false
	}
	cfg := scope.ConfigStore.Get()
	exchange := tvbottrading.NormalizeExchange(cfg.Trading.TargetExchange)
	if !tvbottrading.ValidTargetExchange(exchange) {
		return "", "", "", false
	}
	env := strings.ToLower(strings.TrimSpace(cfg.Trading.TargetTradeEnv))
	if env != tvbottrading.TradeEnvDemo && env != tvbottrading.TradeEnvLive {
		env = strings.ToLower(strings.TrimSpace(cfg.Trading.Env))
	}
	if env != tvbottrading.TradeEnvDemo && env != tvbottrading.TradeEnvLive {
		return "", "", "", false
	}
	return exchange, strings.TrimSpace(cfg.Trading.TargetAPIID), env, true
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

func (m *tenantManager) PruneRetention(ctx context.Context, tradeMonitorBefore, analysisCacheBefore time.Time) (tradeMonitorEvents, analysisCache int64, err error) {
	if m == nil || strings.TrimSpace(m.rootPath) == "" {
		return 0, 0, nil
	}
	m.mu.Lock()
	resources := make([]*tenantResource, 0, len(m.resources))
	knownPaths := make(map[string]struct{}, len(m.resources))
	for _, resource := range m.resources {
		if resource == nil || resource.orders == nil {
			continue
		}
		resources = append(resources, resource)
		knownPaths[filepath.Clean(filepath.Join(m.rootPath, userDirectoryName(resource.username), "tvbot.db"))] = struct{}{}
	}
	rootPath := m.rootPath
	m.mu.Unlock()

	prune := func(orders *tvbotstorage.OrderStore) {
		if orders == nil || err != nil || ctx.Err() != nil {
			return
		}
		var events, cache int64
		events, cache, err = orders.PruneRetention(ctx, tradeMonitorBefore, analysisCacheBefore)
		tradeMonitorEvents += events
		analysisCache += cache
	}
	for _, resource := range resources {
		prune(resource.orders)
	}
	if err != nil || ctx.Err() != nil {
		return tradeMonitorEvents, analysisCache, err
	}

	paths, globErr := filepath.Glob(filepath.Join(rootPath, "*", "tvbot.db"))
	if globErr != nil {
		return tradeMonitorEvents, analysisCache, globErr
	}
	for _, path := range paths {
		if _, known := knownPaths[filepath.Clean(path)]; known {
			continue
		}
		if ctx.Err() != nil {
			return tradeMonitorEvents, analysisCache, ctx.Err()
		}
		orders, openErr := tvbotstorage.NewSQLiteOrderStore(path, "")
		if openErr != nil {
			if err == nil {
				err = fmt.Errorf("open tenant order store %s: %w", path, openErr)
			}
			continue
		}
		prune(orders)
		if closeErr := orders.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close tenant order store %s: %w", path, closeErr)
		}
		if err != nil {
			break
		}
	}
	return tradeMonitorEvents, analysisCache, err
}

func tenantIDForUser(username string) string {
	return "u_" + base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(username)))
}

func userForTenantID(tenantID string) (string, error) {
	tenantID = strings.TrimSpace(tenantID)
	if !strings.HasPrefix(tenantID, "u_") {
		// The liquidation alert service uses the authenticated username as its
		// tenant key. Accept that internal form in addition to the encoded ID
		// emitted by TVBot templates so alert webhooks reach the same tenant
		// order store instead of falling back to the default owner.
		if tenantID == "" || strings.ContainsAny(tenantID, `/\\`) {
			return "", errors.New("tenant_id has an invalid format")
		}
		return tenantID, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(tenantID, "u_"))
	if err != nil || strings.TrimSpace(string(decoded)) == "" {
		return "", errors.New("tenant_id has an invalid format")
	}
	return strings.TrimSpace(string(decoded)), nil
}
