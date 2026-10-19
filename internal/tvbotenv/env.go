package env

import (
	"errors"
	"os"
)

type Secrets struct {
	OKXAPIKey        string
	OKXSecretKey     string
	OKXPassphrase    string
	BinanceAPIKey    string
	BinanceSecretKey string
	TVTokenSecret    string
	AdminToken       string
	AdminUser        string
	AdminPassword    string
}

func Load() Secrets {
	s := Secrets{
		OKXAPIKey:        os.Getenv("OKX_API_KEY"),
		OKXSecretKey:     os.Getenv("OKX_SECRET_KEY"),
		OKXPassphrase:    os.Getenv("OKX_PASSPHRASE"),
		BinanceAPIKey:    os.Getenv("BINANCE_API_KEY"),
		BinanceSecretKey: os.Getenv("BINANCE_SECRET_KEY"),
		TVTokenSecret:    os.Getenv("TV_TOKEN_SECRET"),
		AdminToken:       os.Getenv("ADMIN_TOKEN"),
		AdminUser:        os.Getenv("ADMIN_USER"),
		AdminPassword:    os.Getenv("ADMIN_PASSWORD"),
	}
	if s.AdminUser == "" {
		s.AdminUser = "admin"
	}
	if s.AdminPassword == "" {
		s.AdminPassword = "Admin123"
	}
	return s
}

func (s Secrets) RequireTVTokenSecret() error {
	if s.TVTokenSecret == "" {
		return errors.New("TV_TOKEN_SECRET is required")
	}
	return nil
}

func (s Secrets) RequireAdminToken() error {
	if s.AdminToken == "" && (s.AdminUser == "" || s.AdminPassword == "") {
		return errors.New("ADMIN_TOKEN or ADMIN_USER/ADMIN_PASSWORD is required")
	}
	return nil
}

func (s Secrets) RequireOKXCredentials() error {
	if s.OKXAPIKey == "" || s.OKXSecretKey == "" || s.OKXPassphrase == "" {
		return errors.New("OKX_API_KEY, OKX_SECRET_KEY and OKX_PASSPHRASE are required")
	}
	return nil
}

func (s Secrets) RequireBinanceCredentials() error {
	if s.BinanceAPIKey == "" || s.BinanceSecretKey == "" {
		return errors.New("BINANCE_API_KEY and BINANCE_SECRET_KEY are required")
	}
	return nil
}
