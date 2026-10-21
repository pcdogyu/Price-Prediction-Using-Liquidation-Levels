package gate

import (
	"path/filepath"
	"testing"
)

func TestCredentialStoreSaveSingleAccountRemovesOtherAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gate-credentials.json")
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
	if _, _, err := store.GateCredentials("backup"); err == nil {
		t.Fatal("backup account should be removed")
	}
	reloaded, err := NewCredentialStore(path, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	creds, id, err := reloaded.GateCredentials("")
	if err != nil || id != "primary" || creds.APIKey != "primary-key" {
		t.Fatalf("reloaded single account id=%q creds=%#v err=%v", id, creds, err)
	}
}
