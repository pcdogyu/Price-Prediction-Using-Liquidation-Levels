package tvbotbridge

import (
	"os"
	"path/filepath"
	"testing"

	tvbotbinance "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbinance"
	tvbotokx "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotokx"
)

func TestUserCredentialResolverIsolatesUsersAndMigratesOwner(t *testing.T) {
	root := t.TempDir()
	legacyOKX := filepath.Join(root, "legacy-okx.json")
	legacyBinance := filepath.Join(root, "legacy-binance.json")
	if err := os.WriteFile(legacyOKX, []byte(`{"active_id":"default","credentials":[{"id":"default","name":"owner","api_key":"owner-okx-key","secret_key":"owner-okx-secret","passphrase":"owner-okx-pass"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyBinance, []byte(`{"active_id":"default","credentials":[{"id":"default","name":"owner","api_key":"owner-binance-key","secret_key":"owner-binance-secret"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	resolver, err := newUserCredentialResolver(root, "admin", legacyOKX, legacyBinance, tvbotokx.Credentials{}, tvbotbinance.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := resolver.ForUser("admin")
	if err != nil {
		t.Fatal(err)
	}
	guest, err := resolver.ForUser("guest")
	if err != nil {
		t.Fatal(err)
	}
	if !admin.OKX.Status().Configured || admin.OKX.Status().APIKeyMasked != "owne...-key" {
		t.Fatalf("owner OKX credentials were not migrated: %#v", admin.OKX.Status())
	}
	if !admin.Binance.Status().Configured || admin.Binance.Status().APIKeyMasked != "owne...-key" {
		t.Fatalf("owner Binance credentials were not migrated: %#v", admin.Binance.Status())
	}
	if guest.OKX.Status().Configured || guest.Binance.Status().Configured {
		t.Fatalf("new user inherited owner credentials: okx=%#v binance=%#v", guest.OKX.Status(), guest.Binance.Status())
	}

	if _, err := guest.OKX.UpdateAccount(tvbotokx.CredentialAccountUpdate{ID: "guest", Active: true, Credentials: tvbotokx.Credentials{APIKey: "guest-okx-key", SecretKey: "guest-okx-secret", Passphrase: "guest-okx-pass"}}); err != nil {
		t.Fatal(err)
	}
	if admin.OKX.Status().APIKeyMasked == guest.OKX.Status().APIKeyMasked {
		t.Fatal("owner and guest OKX credentials share state")
	}
	if admin.OKX.Status().APIKeyMasked != "owne...-key" {
		t.Fatal("guest update changed owner OKX credentials")
	}

	reloaded, err := newUserCredentialResolver(root, "admin", legacyOKX, legacyBinance, tvbotokx.Credentials{}, tvbotbinance.Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	reloadedGuest, err := reloaded.ForUser("guest")
	if err != nil {
		t.Fatal(err)
	}
	if reloadedGuest.OKX.Status().APIKeyMasked != "gues...-key" {
		t.Fatalf("guest credentials did not persist: %#v", reloadedGuest.OKX.Status())
	}
	if userDirectoryName("admin") == userDirectoryName("guest") {
		t.Fatal("user credential directories collide")
	}
}
