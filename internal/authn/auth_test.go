package authn

import (
	"errors"
	"testing"
	"time"
)

func TestPasswordSessionAndLockout(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	m, err := New("pcdog", hash, "/liquidation/")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	if _, _, err = m.Login("1.2.3.4", "pcdog", "wrong"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wrong password error=%v", err)
	}
	token, expires, err := m.Login("2.3.4.5", "pcdog", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Authenticated(token) || expires.Sub(now) != 8*time.Hour {
		t.Fatal("valid session was not created")
	}
	restarted, err := New("pcdog", hash, "/liquidation/")
	if err != nil || restarted.Authenticated(token) {
		t.Fatal("session survived a manager restart")
	}
	now = now.Add(8*time.Hour + time.Second)
	if m.Authenticated(token) {
		t.Fatal("expired session is still valid")
	}
	now = now.Add(-8*time.Hour - time.Second)
	token, _, err = m.Login("2.3.4.5", "pcdog", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	m.Logout(token)
	if m.Authenticated(token) {
		t.Fatal("logged out session is still valid")
	}
	for i := 0; i < 5; i++ {
		_, _, _ = m.Login("5.6.7.8", "pcdog", "wrong")
	}
	if _, _, err = m.Login("5.6.7.8", "pcdog", "correct horse battery staple"); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked login error=%v", err)
	}
	now = now.Add(16 * time.Minute)
	if _, _, err = m.Login("5.6.7.8", "pcdog", "correct horse battery staple"); err != nil {
		t.Fatalf("login after lock expiry: %v", err)
	}
}

func TestDisabledAndBasePath(t *testing.T) {
	m, err := New("", "", "liquidation")
	if err != nil || m.Enabled() || m.BasePath() != "/liquidation/" {
		t.Fatalf("manager=%+v error=%v", m, err)
	}
	if !m.Authenticated("") {
		t.Fatal("disabled authentication should allow requests")
	}
}
