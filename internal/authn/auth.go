package authn

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
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

type attempt struct {
	windowStart time.Time
	failures    int
	lockedUntil time.Time
}

type Manager struct {
	username     string
	passwordHash string
	basePath     string
	now          func() time.Time
	ttl          time.Duration
	mu           sync.Mutex
	sessions     map[string]session
	attempts     map[string]attempt
	hashSlots    chan struct{}
}

func New(username, passwordHash, basePath string) (*Manager, error) {
	basePath = normalizeBasePath(basePath)
	m := &Manager{
		username:     strings.TrimSpace(username),
		passwordHash: strings.TrimSpace(passwordHash),
		basePath:     basePath,
		now:          time.Now,
		ttl:          sessionTTL,
		sessions:     make(map[string]session),
		attempts:     make(map[string]attempt),
		hashSlots:    make(chan struct{}, 2),
	}
	if !m.Enabled() {
		if m.username != "" || m.passwordHash != "" {
			return nil, errors.New("both APP_AUTH_USERNAME and APP_AUTH_PASSWORD_HASH are required")
		}
		return m, nil
	}
	if _, err := parseHash(m.passwordHash); err != nil {
		return nil, fmt.Errorf("invalid APP_AUTH_PASSWORD_HASH: %w", err)
	}
	return m, nil
}

func (m *Manager) Enabled() bool    { return m.username != "" && m.passwordHash != "" }
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
	expectedUser := sha256.Sum256([]byte(m.username))
	validUser := subtle.ConstantTimeCompare(providedUser[:], expectedUser[:]) == 1
	validPassword := verifyPassword(password, m.passwordHash)
	if !validUser || !validPassword {
		m.recordFailure(ip, now)
		return "", time.Time{}, ErrInvalid
	}

	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", time.Time{}, fmt.Errorf("generate session: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expires := now.Add(m.ttl)
	m.mu.Lock()
	delete(m.attempts, ip)
	m.pruneSessionsLocked(now)
	m.sessions[token] = session{expires: expires}
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
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	if !ok || !now.Before(s.expires) {
		delete(m.sessions, token)
		return false
	}
	return true
}

func (m *Manager) Logout(token string) {
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
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
