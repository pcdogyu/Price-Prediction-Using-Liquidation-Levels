package okx

import (
	"path/filepath"
	"testing"
)

func TestCredentialStoreUpdateMasksAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "okx-credentials.json")
	store, err := NewCredentialStore(path, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.Update(Credentials{
		APIKey:     "abcd12345678wxyz",
		SecretKey:  "secret",
		Passphrase: "pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !status.Configured || status.APIKeyMasked != "abcd...wxyz" || !status.SecretKeySet || !status.PassphraseSet {
		t.Fatalf("bad status: %#v", status)
	}
	reloaded, err := NewCredentialStore(path, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	creds, id, err := reloaded.OKXCredentials("")
	if err != nil {
		t.Fatal(err)
	}
	if id != DefaultCredentialID {
		t.Fatalf("default id = %q", id)
	}
	if creds.APIKey != "abcd12345678wxyz" || creds.SecretKey != "secret" || creds.Passphrase != "pass" {
		t.Fatalf("bad reloaded credentials: %#v", creds)
	}
}

func TestCredentialStoreSupportsMultipleAccountsAndActiveSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "okx-credentials.json")
	store, err := NewCredentialStore(path, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAccount(CredentialAccountUpdate{
		ID:     "Main Live",
		Name:   "main",
		Active: true,
		Credentials: Credentials{
			APIKey:     "main-key",
			SecretKey:  "main-secret",
			Passphrase: "main-pass",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAccount(CredentialAccountUpdate{
		ID:     "Backup",
		Name:   "backup",
		Active: false,
		Credentials: Credentials{
			APIKey:     "backup-key",
			SecretKey:  "backup-secret",
			Passphrase: "backup-pass",
		},
	}); err != nil {
		t.Fatal(err)
	}
	status := store.Status()
	if status.ActiveID != "main-live" || len(status.Credentials) != 2 {
		t.Fatalf("bad status: %#v", status)
	}
	creds, id, err := store.OKXCredentials("backup")
	if err != nil {
		t.Fatal(err)
	}
	if id != "backup" || creds.APIKey != "backup-key" {
		t.Fatalf("bad selected credentials id=%q creds=%#v", id, creds)
	}
	reloaded, err := NewCredentialStore(path, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	defaultCreds, activeID, err := reloaded.OKXCredentials("")
	if err != nil {
		t.Fatal(err)
	}
	if activeID != "main-live" || defaultCreds.APIKey != "main-key" {
		t.Fatalf("bad reloaded active id=%q creds=%#v", activeID, defaultCreds)
	}
}

func TestCredentialStoreRenameAccountPreservesCredentialsAndActiveSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "okx-credentials.json")
	store, err := NewCredentialStore(path, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAccount(CredentialAccountUpdate{
		ID:     "Main Live",
		Name:   "main",
		Active: true,
		Credentials: Credentials{
			APIKey:     "main-key",
			SecretKey:  "main-secret",
			Passphrase: "main-pass",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAccount(CredentialAccountUpdate{
		ID:     "backup",
		Active: false,
		Credentials: Credentials{
			APIKey:     "backup-key",
			SecretKey:  "backup-secret",
			Passphrase: "backup-pass",
		},
	}); err != nil {
		t.Fatal(err)
	}
	status, err := store.RenameAccount("main-live", "Primary API")
	if err != nil {
		t.Fatal(err)
	}
	if status.ActiveID != "primary-api" || len(status.Credentials) != 2 {
		t.Fatalf("bad rename status: %#v", status)
	}
	if _, err := store.RenameAccount("primary-api", "backup"); err == nil {
		t.Fatal("expected duplicate API ID error")
	}
	reloaded, err := NewCredentialStore(path, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	creds, id, err := reloaded.OKXCredentials("")
	if err != nil {
		t.Fatal(err)
	}
	if id != "primary-api" || creds.APIKey != "main-key" || creds.SecretKey != "main-secret" || creds.Passphrase != "main-pass" {
		t.Fatalf("bad renamed active credentials id=%q creds=%#v", id, creds)
	}
	if _, _, err := reloaded.OKXCredentials("main-live"); err == nil {
		t.Fatal("old API ID should not resolve after rename")
	}
}
