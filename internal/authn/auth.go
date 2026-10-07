package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	memoryKiB  = 64 * 1024
	iterations = 3
	parallel   = 2
	saltBytes  = 16
	keyBytes   = 32
	sessionTTL = 7 * 24 * time.Hour
)

var (
	ErrInvalid = errors.New("invalid credentials")
	ErrLocked  = errors.New("too many login attempts")
)

type session struct {
	expires time.Time
}

// SessionStore persists only hashes of session tokens. The raw bearer token
// remains in the browser cookie and is never written to disk.
type SessionStore interface {
	LoadAuthSessions(context.Context, time.Time) (map[string]time.Time, error)
	SaveAuthSession(context.Context, string, time.Time) error
	DeleteAuthSession(context.Context, string) error
}

type UserStore interface {
	LoadAuthUsers(context.Context) (map[string]string, error)
}

type attempt struct {
	windowStart time.Time
	failures    int
	lockedUntil time.Time
}

type Manager struct {
	username     string
	passwordHash string
	credentials  map[string]string
	basePath     string
	now          func() time.Time
	ttl          time.Duration
	mu           sync.Mutex
	sessions     map[string]session
	attempts     map[string]attempt
	hashSlots    chan struct{}
	store        SessionStore
	namespace    string
}

func New(username, passwordHash, basePath string, stores ...SessionStore) (*Manager, error) {
	if len(stores) > 1 {
		return nil, errors.New("at most one session store may be configured")
	}
	basePath = normalizeBasePath(basePath)
	m := &Manager{
		username:     strings.TrimSpace(username),
		passwordHash: strings.TrimSpace(passwordHash),
		credentials:  make(map[string]string),
		basePath:     basePath,
		now:          time.Now,
		ttl:          sessionTTL,
		sessions:     make(map[string]session),
		attempts:     make(map[string]attempt),
		hashSlots:    make(chan struct{}, 2),
	}
	if len(stores) == 1 {
		m.store = stores[0]
	}
	if (m.username == "") != (m.passwordHash == "") {
		return nil, errors.New("both APP_AUTH_USERNAME and APP_AUTH_PASSWORD_HASH are required")
	}
	if userStore, ok := m.store.(UserStore); ok {
		storedUsers, err := userStore.LoadAuthUsers(context.Background())
		if err != nil {
			return nil, fmt.Errorf("load authentication users: %w", err)
		}
		for storedUsername, storedHash := range storedUsers {
			storedUsername = strings.TrimSpace(storedUsername)
			storedHash = strings.TrimSpace(storedHash)
			if storedUsername != "" && storedHash != "" {
				m.credentials[storedUsername] = storedHash
			}
		}
	}
	if m.username != "" {
		m.credentials[m.username] = m.passwordHash
	}
	if !m.Enabled() {
		return m, nil
	}
	for account, encoded := range m.credentials {
		if _, err := parseHash(encoded); err != nil {
			return nil, fmt.Errorf("invalid password hash for authentication user %q: %w", account, err)
		}
	}
	if m.passwordHash == "" {
		accounts := make([]string, 0, len(m.credentials))
		for account := range m.credentials {
			accounts = append(accounts, account)
		}
		sort.Strings(accounts)
		m.username = accounts[0]
		m.passwordHash = m.credentials[m.username]
	}
	identity := sha256.Sum256([]byte(m.username + "\x00" + m.passwordHash))
	m.namespace = base64.RawURLEncoding.EncodeToString(identity[:16]) + "."
	if m.store != nil {
		stored, err := m.store.LoadAuthSessions(context.Background(), m.now().UTC())
		if err != nil {
			return nil, fmt.Errorf("load authentication sessions: %w", err)
		}
		for key, expires := range stored {
			if strings.HasPrefix(key, m.namespace) && m.now().UTC().Before(expires) {
				m.sessions[key] = session{expires: expires}
			}
		}
	}
	return m, nil
}

func (m *Manager) Enabled() bool    { return len(m.credentials) > 0 }
func (m *Manager) BasePath() string { return m.basePath }

func (m *Manager) Login(ip, username, password string) (string, time.Time, error) {
	if !m.Enabled() {
		return "", time.Time{}, errors.New("authentication is disabled")
	}
	now := m.now().UTC()
	m.mu.Lock()
	a := m.attempts[ip]
	if now.Before(a.lockedUntil) {
		m.mu.Unlock()
		return "", time.Time{}, ErrLocked
	}
	m.mu.Unlock()

	select {
	case m.hashSlots <- struct{}{}:
		defer func() { <-m.hashSlots }()
	default:
		return "", time.Time{}, ErrLocked
	}
	providedUser := sha256.Sum256([]byte(username))
	selectedHash := m.passwordHash
	validUser := 0
	for account, encoded := range m.credentials {
		expectedUser := sha256.Sum256([]byte(account))
		matches := subtle.ConstantTimeCompare(providedUser[:], expectedUser[:])
		validUser |= matches
		if matches == 1 {
			selectedHash = encoded
		}
	}
	validPassword := verifyPassword(password, selectedHash)
	if validUser != 1 || !validPassword {
		m.recordFailure(ip, now)
		return "", time.Time{}, ErrInvalid
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", time.Time{}, fmt.Errorf("generate session: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expires := now.Add(m.ttl)
	key := m.sessionKey(token)
	if m.store != nil {
		if err := m.store.SaveAuthSession(context.Background(), key, expires); err != nil {
			return "", time.Time{}, fmt.Errorf("persist session: %w", err)
		}
	}
	m.mu.Lock()
	delete(m.attempts, ip)
	m.pruneSessionsLocked(now)
	m.sessions[key] = session{expires: expires}
	m.mu.Unlock()
	return token, expires, nil
}

func (m *Manager) Authenticated(token string) bool {
	if !m.Enabled() {
		return true
	}
	if token == "" {
		return false
	}
	key := m.sessionKey(token)
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[key]
	if !ok || !now.Before(s.expires) {
		delete(m.sessions, key)
		return false
	}
	return true
}

func (m *Manager) Logout(token string) {
	key := m.sessionKey(token)
	m.mu.Lock()
	delete(m.sessions, key)
	m.mu.Unlock()
	if m.store != nil {
		_ = m.store.DeleteAuthSession(context.Background(), key)
	}
}

func (m *Manager) sessionKey(token string) string {
	digest := sha256.Sum256([]byte(token))
	return m.namespace + base64.RawURLEncoding.EncodeToString(digest[:])
}

func (m *Manager) recordFailure(ip string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for address, old := range m.attempts {
		if now.After(old.lockedUntil) && now.Sub(old.windowStart) >= 30*time.Minute {
			delete(m.attempts, address)
		}
	}
	a := m.attempts[ip]
	if a.windowStart.IsZero() || now.Sub(a.windowStart) >= 15*time.Minute {
		a = attempt{windowStart: now}
	}
	a.failures++
	if a.failures >= 5 {
		a.lockedUntil = now.Add(15 * time.Minute)
	}
	m.attempts[ip] = a
}

func (m *Manager) pruneSessionsLocked(now time.Time) {
	for token, s := range m.sessions {
		if !now.Before(s.expires) {
			delete(m.sessions, token)
		}
	}
}

func normalizeBasePath(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "/" {
		return "/"
	}
	return "/" + strings.Trim(v, "/") + "/"
}

type hashParams struct {
	memory     uint32
	iterations uint32
	parallel   uint8
	salt       []byte
	key        []byte
}

func HashPassword(password string) (string, error) {
	salt := make([]byte, saltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, iterations, memoryKiB, parallel, keyBytes)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memoryKiB, iterations, parallel,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(password, encoded string) bool {
	p, err := parseHash(encoded)
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), p.salt, p.iterations, p.memory, p.parallel, uint32(len(p.key)))
	return subtle.ConstantTimeCompare(actual, p.key) == 1
}

func parseHash(encoded string) (hashParams, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return hashParams{}, errors.New("expected Argon2id PHC string")
	}
	var p hashParams
	for _, item := range strings.Split(parts[3], ",") {
		kv := strings.SplitN(item, "=", 2)
		if len(kv) != 2 {
			return hashParams{}, errors.New("invalid Argon2 parameters")
		}
		n, err := strconv.ParseUint(kv[1], 10, 32)
		if err != nil {
			return hashParams{}, errors.New("invalid Argon2 parameter value")
		}
		switch kv[0] {
		case "m":
			p.memory = uint32(n)
		case "t":
			p.iterations = uint32(n)
		case "p":
			if n > 255 {
				return hashParams{}, errors.New("invalid Argon2 parallelism")
			}
			p.parallel = uint8(n)
		}
	}
	if p.memory != memoryKiB || p.iterations != iterations || p.parallel != parallel {
		return hashParams{}, errors.New("unsupported Argon2 parameters")
	}
	var err error
	if p.salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil || len(p.salt) != saltBytes {
		return hashParams{}, errors.New("invalid Argon2 salt")
	}
	if p.key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil || len(p.key) != keyBytes {
		return hashParams{}, errors.New("invalid Argon2 key")
	}
	return p, nil
}
