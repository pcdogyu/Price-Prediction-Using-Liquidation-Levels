package okx

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
	OKXCredentials(apiID string) (Credentials, string, error)
}

type CredentialStore struct {
	mu       sync.RWMutex
	path     string
	accounts map[string]credentialAccount
	activeID string
}

type CredentialStatus struct {
	Configured    bool                      `json:"configured"`
	ActiveID      string                    `json:"active_id,omitempty"`
	APIKeyMasked  string                    `json:"api_key_masked,omitempty"`
	SecretKeySet  bool                      `json:"secret_key_set"`
	PassphraseSet bool                      `json:"passphrase_set"`
	Source        string                    `json:"source,omitempty"`
	UpdatedAt     time.Time                 `json:"updated_at,omitempty"`
	Credentials   []CredentialAccountStatus `json:"credentials,omitempty"`
}

type CredentialAccountStatus struct {
	ID            string    `json:"id"`
	Name          string    `json:"name,omitempty"`
	Configured    bool      `json:"configured"`
	Active        bool      `json:"active"`
	APIKeyMasked  string    `json:"api_key_masked,omitempty"`
	SecretKeySet  bool      `json:"secret_key_set"`
	PassphraseSet bool      `json:"passphrase_set"`
	Source        string    `json:"source,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
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
	ActiveID    string                `json:"active_id"`
	Credentials []credentialFileEntry `json:"credentials"`

	// Legacy single-account format.
	APIKey     string    `json:"api_key"`
	SecretKey  string    `json:"secret_key"`
	Passphrase string    `json:"passphrase"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type credentialFileEntry struct {
	ID         string    `json:"id"`
	Name       string    `json:"name,omitempty"`
	APIKey     string    `json:"api_key"`
	SecretKey  string    `json:"secret_key"`
	Passphrase string    `json:"passphrase"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func NewCredentialStore(path string, initial Credentials) (*CredentialStore, error) {
	s := &CredentialStore{
		path:     path,
		accounts: map[string]credentialAccount{},
	}
	initial = trimCredentials(initial)
	if credentialsConfigured(initial) {
		now := time.Now().UTC()
		s.accounts[DefaultCredentialID] = credentialAccount{
			ID:          DefaultCredentialID,
			Name:        "Environment",
			Credentials: initial,
			Source:      "env",
			UpdatedAt:   now,
		}
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

func (s *CredentialStore) OKXCredentials(apiID string) (Credentials, string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id := s.resolveIDLocked(apiID)
	account, ok := s.accounts[id]
	if !ok || !credentialsConfigured(account.Credentials) {
		if strings.TrimSpace(apiID) == "" {
			return Credentials{}, id, errors.New("default OKX API credentials are not configured")
		}
		return Credentials{}, id, fmt.Errorf("OKX API %q is not configured", strings.TrimSpace(apiID))
	}
	return account.Credentials, account.ID, nil
}

func (s *CredentialStore) Status() CredentialStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.statusLocked()
}

func (s *CredentialStore) Update(creds Credentials) (CredentialStatus, error) {
	return s.UpdateAccount(CredentialAccountUpdate{
		ID:          DefaultCredentialID,
		Credentials: creds,
		Active:      true,
	})
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
		if creds.Passphrase == "" {
			creds.Passphrase = existing.Credentials.Passphrase
		}
	}
	if !credentialsConfigured(creds) {
		return CredentialStatus{}, errors.New("api_key, secret_key and passphrase are required")
	}
	name := strings.TrimSpace(update.Name)
	if name == "" && found {
		name = existing.Name
	}
	if name == "" {
		name = id
	}
	s.accounts[id] = credentialAccount{
		ID:          id,
		Name:        name,
		Credentials: creds,
		Source:      "file",
		UpdatedAt:   time.Now().UTC(),
	}
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
		return CredentialStatus{}, fmt.Errorf("OKX API %q is not configured", oldID)
	}
	if oldID == newID {
		return s.statusLocked(), nil
	}
	if _, exists := s.accounts[newID]; exists {
		return CredentialStatus{}, fmt.Errorf("OKX API %q already exists", newID)
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
		return CredentialStatus{}, fmt.Errorf("OKX API %q is not configured", id)
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
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
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
		creds := trimCredentials(Credentials{
			APIKey:     entry.APIKey,
			SecretKey:  entry.SecretKey,
			Passphrase: entry.Passphrase,
		})
		if !credentialsConfigured(creds) {
			continue
		}
		updatedAt := entry.UpdatedAt.UTC()
		if updatedAt.IsZero() {
			updatedAt = time.Now().UTC()
		}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = id
		}
		loaded[id] = credentialAccount{
			ID:          id,
			Name:        name,
			Credentials: creds,
			Source:      "file",
			UpdatedAt:   updatedAt,
		}
	}
	if len(loaded) == 0 {
		creds := trimCredentials(Credentials{
			APIKey:     file.APIKey,
			SecretKey:  file.SecretKey,
			Passphrase: file.Passphrase,
		})
		if credentialsConfigured(creds) {
			updatedAt := file.UpdatedAt.UTC()
			if updatedAt.IsZero() {
				updatedAt = time.Now().UTC()
			}
			loaded[DefaultCredentialID] = credentialAccount{
				ID:          DefaultCredentialID,
				Name:        DefaultCredentialID,
				Credentials: creds,
				Source:      "file",
				UpdatedAt:   updatedAt,
			}
		}
	}
	if len(loaded) == 0 {
		return nil
	}
	s.accounts = loaded
	s.activeID = normalizeCredentialID(file.ActiveID)
	if !s.hasConfiguredLocked(s.activeID) {
		s.activeID = s.firstConfiguredIDLocked()
	}
	return nil
}

func (s *CredentialStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil && filepath.Dir(s.path) != "." {
		return err
	}
	file := credentialFile{
		ActiveID:    s.activeID,
		Credentials: []credentialFileEntry{},
	}
	for _, account := range s.sortedAccountsLocked() {
		if !credentialsConfigured(account.Credentials) {
			continue
		}
		file.Credentials = append(file.Credentials, credentialFileEntry{
			ID:         account.ID,
			Name:       account.Name,
			APIKey:     account.Credentials.APIKey,
			SecretKey:  account.Credentials.SecretKey,
			Passphrase: account.Credentials.Passphrase,
			UpdatedAt:  account.UpdatedAt,
		})
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
	accounts := make([]CredentialAccountStatus, 0, len(s.accounts))
	for _, account := range s.sortedAccountsLocked() {
		status := credentialAccountStatus(account, account.ID == s.activeID)
		accounts = append(accounts, status)
	}
	active, ok := s.accounts[s.activeID]
	if !ok {
		return CredentialStatus{Credentials: accounts}
	}
	status := credentialStatus(active.Credentials, active.Source, active.UpdatedAt)
	status.ActiveID = active.ID
	status.Credentials = accounts
	return status
}

func (s *CredentialStore) resolveIDLocked(apiID string) string {
	id := normalizeCredentialID(apiID)
	if strings.TrimSpace(apiID) == "" && s.activeID != "" {
		id = s.activeID
	}
	return id
}

func (s *CredentialStore) hasConfiguredLocked(id string) bool {
	account, ok := s.accounts[id]
	return ok && credentialsConfigured(account.Credentials)
}

func (s *CredentialStore) firstConfiguredIDLocked() string {
	for _, account := range s.sortedAccountsLocked() {
		if credentialsConfigured(account.Credentials) {
			return account.ID
		}
	}
	return ""
}

func (s *CredentialStore) sortedAccountsLocked() []credentialAccount {
	accounts := make([]credentialAccount, 0, len(s.accounts))
	for _, account := range s.accounts {
		accounts = append(accounts, account)
	}
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].ID == s.activeID {
			return true
		}
		if accounts[j].ID == s.activeID {
			return false
		}
		return accounts[i].ID < accounts[j].ID
	})
	return accounts
}

func credentialStatus(creds Credentials, source string, updatedAt time.Time) CredentialStatus {
	return CredentialStatus{
		Configured:    credentialsConfigured(creds),
		APIKeyMasked:  maskAPIKey(creds.APIKey),
		SecretKeySet:  strings.TrimSpace(creds.SecretKey) != "",
		PassphraseSet: strings.TrimSpace(creds.Passphrase) != "",
		Source:        source,
		UpdatedAt:     updatedAt,
	}
}

func credentialAccountStatus(account credentialAccount, active bool) CredentialAccountStatus {
	return CredentialAccountStatus{
		ID:            account.ID,
		Name:          account.Name,
		Configured:    credentialsConfigured(account.Credentials),
		Active:        active,
		APIKeyMasked:  maskAPIKey(account.Credentials.APIKey),
		SecretKeySet:  strings.TrimSpace(account.Credentials.SecretKey) != "",
		PassphraseSet: strings.TrimSpace(account.Credentials.Passphrase) != "",
		Source:        account.Source,
		UpdatedAt:     account.UpdatedAt,
	}
}

func trimCredentials(creds Credentials) Credentials {
	return Credentials{
		APIKey:     strings.TrimSpace(creds.APIKey),
		SecretKey:  strings.TrimSpace(creds.SecretKey),
		Passphrase: strings.TrimSpace(creds.Passphrase),
	}
}

func credentialsConfigured(creds Credentials) bool {
	return strings.TrimSpace(creds.APIKey) != "" &&
		strings.TrimSpace(creds.SecretKey) != "" &&
		strings.TrimSpace(creds.Passphrase) != ""
}

func normalizeCredentialID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return DefaultCredentialID
	}
	id = strings.ToLower(id)
	var b strings.Builder
	lastDash := false
	for _, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return DefaultCredentialID
	}
	return out
}

func maskAPIKey(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	if len(apiKey) <= 8 {
		return "****" + apiKey
	}
	return apiKey[:4] + "..." + apiKey[len(apiKey)-4:]
}
