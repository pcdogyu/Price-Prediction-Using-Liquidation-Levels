package binance

import (
	"path/filepath"
	"testing"
)

func TestCredentialStoreUsesActiveAccountWhenAPIIDIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binance-credentials.json")
	store, err := NewCredentialStore(path, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAccount(CredentialAccountUpdate{
		ID:     "tvbot",
		Name:   "binance-moni",
		Active: true,
		Credentials: Credentials{
			APIKey:    "active-key",
			SecretKey: "active-secret",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAccount(CredentialAccountUpdate{
		ID:     "backup",
		Name:   "backup",
		Active: false,
		Credentials: Credentials{
			APIKey:    "backup-key",
			SecretKey: "backup-secret",
		},
	}); err != nil {
		t.Fatal(err)
	}
	creds, id, err := store.BinanceCredentials("")
	if err != nil {
		t.Fatal(err)
	}
	if id != "tvbot" || creds.APIKey != "active-key" {
		t.Fatalf("empty api id should use active account id=%q creds=%#v", id, creds)
	}
	reloaded, err := NewCredentialStore(path, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	creds, id, err = reloaded.BinanceCredentials("")
	if err != nil {
		t.Fatal(err)
	}
	if id != "tvbot" || creds.APIKey != "active-key" {
		t.Fatalf("reloaded empty api id should use active account id=%q creds=%#v", id, creds)
	}
}

func TestCredentialStoreRenameAccountPreservesCredentialsAndActiveSelection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binance-credentials.json")
	store, err := NewCredentialStore(path, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAccount(CredentialAccountUpdate{
		ID:     "tvbot",
		Name:   "binance-moni",
		Active: true,
		Credentials: Credentials{
			APIKey:    "active-key",
			SecretKey: "active-secret",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateAccount(CredentialAccountUpdate{
		ID:     "backup",
		Active: false,
		Credentials: Credentials{
			APIKey:    "backup-key",
			SecretKey: "backup-secret",
		},
	}); err != nil {
		t.Fatal(err)
	}
	status, err := store.RenameAccount("tvbot", "binance-prod")
	if err != nil {
		t.Fatal(err)
	}
	if status.ActiveID != "binance-prod" || len(status.Credentials) != 2 {
		t.Fatalf("bad rename status: %#v", status)
	}
	if _, err := store.RenameAccount("binance-prod", "backup"); err == nil {
		t.Fatal("expected duplicate API ID error")
	}
	reloaded, err := NewCredentialStore(path, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	creds, id, err := reloaded.BinanceCredentials("")
	if err != nil {
		t.Fatal(err)
	}
	if id != "binance-prod" || creds.APIKey != "active-key" || creds.SecretKey != "active-secret" {
		t.Fatalf("bad renamed active credentials id=%q creds=%#v", id, creds)
	}
	if _, _, err := reloaded.BinanceCredentials("tvbot"); err == nil {
		t.Fatal("old API ID should not resolve after rename")
	}
}

func TestCredentialStoreSaveSingleAccountRemovesOtherAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binance-credentials.json")
	store, err := NewCredentialStore(path, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"main", "backup"} {
		if _, err := store.UpdateAccount(CredentialAccountUpdate{
			ID: id, Active: id == "main",
			Credentials: Credentials{APIKey: id + "-key", SecretKey: id + "-secret"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	status, err := store.SaveSingleAccount(CredentialAccountUpdate{
		ID:              "primary",
		Name:            "Primary",
		Active:          true,
		PreserveMissing: false,
		Credentials:     Credentials{APIKey: "primary-key", SecretKey: "primary-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if status.ActiveID != "primary" || len(status.Credentials) != 1 || status.Credentials[0].ID != "primary" {
		t.Fatalf("single-account status = %#v", status)
	}
	if _, _, err := store.BinanceCredentials("backup"); err == nil {
		t.Fatal("backup account should be removed")
	}
	reloaded, err := NewCredentialStore(path, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	creds, id, err := reloaded.BinanceCredentials("")
	if err != nil || id != "primary" || creds.APIKey != "primary-key" {
		t.Fatalf("reloaded single account id=%q creds=%#v err=%v", id, creds, err)
	}
}
