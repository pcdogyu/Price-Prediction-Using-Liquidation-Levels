package gate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const DefaultCredentialID = "default"

type CredentialProvider interface {
	GateCredentials(apiID string) (Credentials, string, error)
}

type CredentialStore struct {
	mu       sync.RWMutex
	path     string
	accounts map[string]credentialAccount
	activeID string
}

type CredentialStatus struct {
	Configured   bool                      `json:"configured"`
	ActiveID     string                    `json:"active_id,omitempty"`
	APIKeyMasked string                    `json:"api_key_masked,omitempty"`
	SecretKeySet bool                      `json:"secret_key_set"`
	Source       string                    `json:"source,omitempty"`
	UpdatedAt    time.Time                 `json:"updated_at,omitempty"`
	Credentials  []CredentialAccountStatus `json:"credentials,omitempty"`
}
type CredentialAccountStatus struct {
	ID           string    `json:"id"`
	Name         string    `json:"name,omitempty"`
	Configured   bool      `json:"configured"`
	Active       bool      `json:"active"`
	APIKeyMasked string    `json:"api_key_masked,omitempty"`
	SecretKeySet bool      `json:"secret_key_set"`
	Source       string    `json:"source,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
}
type CredentialAccountUpdate struct {
	ID              string
	Name            string
	Credentials     Credentials
	Active          bool
	PreserveMissing bool
}
type credentialAccount struct {
	ID          string
	Name        string
	Credentials Credentials
	Source      string
	UpdatedAt   time.Time
}
type credentialFile struct {
	ActiveID    *string               `json:"active_id"`
	Credentials []credentialFileEntry `json:"credentials"`
	APIKey      string                `json:"api_key"`
	SecretKey   string                `json:"secret_key"`
	UpdatedAt   time.Time             `json:"updated_at"`
}
type credentialFileEntry struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	APIKey    string    `json:"api_key"`
	SecretKey string    `json:"secret_key"`
	UpdatedAt time.Time `json:"updated_at"`
}

func NewCredentialStore(path string, initial Credentials) (*CredentialStore, error) {
	s := &CredentialStore{path: path, accounts: map[string]credentialAccount{}}
	initial = trimCredentials(initial)
	if credentialsConfigured(initial) {
		now := time.Now().UTC()
		s.accounts[DefaultCredentialID] = credentialAccount{ID: DefaultCredentialID, Name: "Environment", Credentials: initial, Source: "env", UpdatedAt: now}
		s.activeID = DefaultCredentialID
	}
	if path == "" {
		return s, nil
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *CredentialStore) GateCredentials(apiID string) (Credentials, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id := s.resolveIDLocked(apiID)
	account, ok := s.accounts[id]
	if !ok || !credentialsConfigured(account.Credentials) {
		if strings.TrimSpace(apiID) == "" {
			return Credentials{}, id, errors.New("default Gate API credentials are not configured")
		}
		return Credentials{}, id, fmt.Errorf("Gate API %q is not configured", strings.TrimSpace(apiID))
	}
	return account.Credentials, account.ID, nil
}

func (s *CredentialStore) Status() CredentialStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.statusLocked()
}

// ClearActive keeps the account credentials but disables this exchange as an
// active trading source and persists that choice across restarts.
func (s *CredentialStore) ClearActive() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeID = ""
	if s.path == "" {
		return nil
	}
	return s.saveLocked()
}

func (s *CredentialStore) UpdateAccount(update CredentialAccountUpdate) (CredentialStatus, error) {
	id := normalizeCredentialID(update.ID)
	creds := trimCredentials(update.Credentials)
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, found := s.accounts[id]
	if update.PreserveMissing && found {
		if creds.APIKey == "" {
			creds.APIKey = existing.Credentials.APIKey
		}
		if creds.SecretKey == "" {
			creds.SecretKey = existing.Credentials.SecretKey
		}
	}
	if !credentialsConfigured(creds) {
		return CredentialStatus{}, errors.New("api_key and secret_key are required")
	}
	name := strings.TrimSpace(update.Name)
	if name == "" && found {
		name = existing.Name
	}
	if name == "" {
		name = id
	}
	s.accounts[id] = credentialAccount{ID: id, Name: name, Credentials: creds, Source: "file", UpdatedAt: time.Now().UTC()}
	if update.Active || s.activeID == "" || !s.hasConfiguredLocked(s.activeID) {
		s.activeID = id
	}
	if s.path != "" {
		if err := s.saveLocked(); err != nil {
			return CredentialStatus{}, err
		}
	}
	return s.statusLocked(), nil
}

// SaveSingleAccount replaces all configured accounts for this exchange and
// environment with the supplied account. It is used by the unified API-key
// page, which intentionally exposes one account per exchange/environment.
func (s *CredentialStore) SaveSingleAccount(update CredentialAccountUpdate) (CredentialStatus, error) {
	id := normalizeCredentialID(update.ID)
	creds := trimCredentials(update.Credentials)

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, found := s.accounts[id]
	if update.PreserveMissing && found {
		if creds.APIKey == "" {
			creds.APIKey = existing.Credentials.APIKey
		}
		if creds.SecretKey == "" {
			creds.SecretKey = existing.Credentials.SecretKey
		}
	}
	if !credentialsConfigured(creds) {
		return CredentialStatus{}, errors.New("api_key and secret_key are required")
	}
	name := strings.TrimSpace(update.Name)
	if name == "" && found {
		name = existing.Name
	}
	if name == "" {
		name = id
	}
	s.accounts = map[string]credentialAccount{id: {
		ID: id, Name: name, Credentials: creds, Source: "file", UpdatedAt: time.Now().UTC(),
	}}
	s.activeID = id
	if s.path != "" {
		if err := s.saveLocked(); err != nil {
			return CredentialStatus{}, err
		}
	}
	return s.statusLocked(), nil
}

func (s *CredentialStore) RenameAccount(oldID, newID string) (CredentialStatus, error) {
	oldID = normalizeCredentialID(oldID)
	if strings.TrimSpace(newID) == "" {
		return CredentialStatus{}, errors.New("new API ID is required")
	}
	newID = normalizeCredentialID(newID)
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[oldID]
	if !ok || !credentialsConfigured(account.Credentials) {
		return CredentialStatus{}, fmt.Errorf("Gate API %q is not configured", oldID)
	}
	if oldID == newID {
		return s.statusLocked(), nil
	}
	if _, exists := s.accounts[newID]; exists {
		return CredentialStatus{}, fmt.Errorf("Gate API %q already exists", newID)
	}
	delete(s.accounts, oldID)
	account.ID = newID
	account.Source = "file"
	account.UpdatedAt = time.Now().UTC()
	s.accounts[newID] = account
	if s.activeID == oldID {
		s.activeID = newID
	}
	if s.path != "" {
		if err := s.saveLocked(); err != nil {
			return CredentialStatus{}, err
		}
	}
	return s.statusLocked(), nil
}

func (s *CredentialStore) DeleteAccount(id string) (CredentialStatus, error) {
	id = normalizeCredentialID(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.accounts[id]; !ok {
		return CredentialStatus{}, fmt.Errorf("Gate API %q is not configured", id)
	}
	delete(s.accounts, id)
	if s.activeID == id {
		s.activeID = s.firstConfiguredIDLocked()
	}
	if s.path != "" {
		if err := s.saveLocked(); err != nil {
			return CredentialStatus{}, err
		}
	}
	return s.statusLocked(), nil
}

func (s *CredentialStore) load() error {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	var file credentialFile
	if err := json.Unmarshal(b, &file); err != nil {
		return err
	}
	loaded := map[string]credentialAccount{}
	for _, entry := range file.Credentials {
		id := normalizeCredentialID(entry.ID)
		creds := trimCredentials(Credentials{APIKey: entry.APIKey, SecretKey: entry.SecretKey})
		if !credentialsConfigured(creds) {
			continue
		}
		updated := entry.UpdatedAt.UTC()
		if updated.IsZero() {
			updated = time.Now().UTC()
		}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = id
		}
		loaded[id] = credentialAccount{ID: id, Name: name, Credentials: creds, Source: "file", UpdatedAt: updated}
	}
	if len(loaded) == 0 {
		creds := trimCredentials(Credentials{APIKey: file.APIKey, SecretKey: file.SecretKey})
		if credentialsConfigured(creds) {
			updated := file.UpdatedAt.UTC()
			if updated.IsZero() {
				updated = time.Now().UTC()
			}
			loaded[DefaultCredentialID] = credentialAccount{ID: DefaultCredentialID, Name: "default", Credentials: creds, Source: "file", UpdatedAt: updated}
		}
	}
	for id, account := range loaded {
		s.accounts[id] = account
	}
	if file.ActiveID != nil {
		active := normalizeCredentialID(*file.ActiveID)
		if active != "" && s.hasConfiguredLocked(active) {
			s.activeID = active
		} else if strings.TrimSpace(*file.ActiveID) == "" {
			s.activeID = ""
		} else if s.activeID == "" {
			s.activeID = s.firstConfiguredIDLocked()
		}
	} else if s.activeID == "" {
		s.activeID = s.firstConfiguredIDLocked()
	}
	return nil
}

func (s *CredentialStore) saveLocked() error {
	activeID := s.activeID
	file := credentialFile{ActiveID: &activeID}
	ids := make([]string, 0, len(s.accounts))
	for id := range s.accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		account := s.accounts[id]
		if !credentialsConfigured(account.Credentials) {
			continue
		}
		file.Credentials = append(file.Credentials, credentialFileEntry{ID: account.ID, Name: account.Name, APIKey: account.Credentials.APIKey, SecretKey: account.Credentials.SecretKey, UpdatedAt: account.UpdatedAt})
	}
	if dir := filepath.Dir(s.path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *CredentialStore) statusLocked() CredentialStatus {
	status := CredentialStatus{}
	ids := make([]string, 0, len(s.accounts))
	for id := range s.accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		account := s.accounts[id]
		row := CredentialAccountStatus{ID: account.ID, Name: account.Name, Configured: credentialsConfigured(account.Credentials), Active: account.ID == s.activeID, APIKeyMasked: maskAPIKey(account.Credentials.APIKey), SecretKeySet: strings.TrimSpace(account.Credentials.SecretKey) != "", Source: account.Source, UpdatedAt: account.UpdatedAt}
		status.Credentials = append(status.Credentials, row)
		if row.Active && row.Configured {
			status.Configured = true
			status.ActiveID = row.ID
			status.APIKeyMasked = row.APIKeyMasked
			status.SecretKeySet = row.SecretKeySet
			status.Source = row.Source
			status.UpdatedAt = row.UpdatedAt
		}
	}
	if !status.Configured {
		status.ActiveID = s.activeID
	}
	return status
}
func (s *CredentialStore) resolveIDLocked(apiID string) string {
	id := strings.TrimSpace(apiID)
	if id != "" {
		return normalizeCredentialID(id)
	}
	if s.activeID != "" {
		return s.activeID
	}
	return DefaultCredentialID
}
func (s *CredentialStore) hasConfiguredLocked(id string) bool {
	account, ok := s.accounts[id]
	return ok && credentialsConfigured(account.Credentials)
}
func (s *CredentialStore) firstConfiguredIDLocked() string {
	ids := make([]string, 0, len(s.accounts))
	for id := range s.accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if s.hasConfiguredLocked(id) {
			return id
		}
	}
	return ""
}
func normalizeCredentialID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return DefaultCredentialID
	}
	return id
}
func trimCredentials(c Credentials) Credentials {
	return Credentials{APIKey: strings.TrimSpace(c.APIKey), SecretKey: strings.TrimSpace(c.SecretKey)}
}
func credentialsConfigured(c Credentials) bool {
	c = trimCredentials(c)
	return c.APIKey != "" && c.SecretKey != ""
}
func maskAPIKey(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if len(v) <= 8 {
		return "****"
	}
	return v[:4] + "..." + v[len(v)-4:]
}
