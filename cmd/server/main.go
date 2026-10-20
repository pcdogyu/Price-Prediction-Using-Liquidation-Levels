package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/app"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/authn"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/observability"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbridge"
	tvbotenv "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotenv"
	tvbotserver "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotserver"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "hash-password" {
		hashPassword()
		return
	}
	if len(os.Args) == 3 && os.Args[1] == "add-user" {
		addUser(os.Args[2])
		return
	}
	cfg := config.Load()
	log, logStore, err := observability.New(cfg.LogPath, cfg.LogRetentionDays)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize application logging: %v\n", err)
		os.Exit(1)
	}
	defer logStore.Close()
	st, err := store.Open(cfg.DatabasePath)
	if err != nil {
		log.Error("database open failed", "error", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := ensureUnifiedAccounts(context.Background(), st, cfg); err != nil {
		log.Error("unified account migration failed", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	svc := app.New(cfg, st, log)
	defer svc.Close()
	svc.Start(ctx)
	bridge, err := tvbotbridge.New(cfg, log, tvbotserver.BuildInfo{
		CommitTime: httpapi.BuildTime, CommitHash: httpapi.BuildRevision, CommitBranch: httpapi.BuildBranch,
	})
	if err != nil {
		log.Error("initialize trading dashboard failed", "error", err)
		os.Exit(1)
	}
	defer bridge.Close()
	svc.SetWebhookTargetResolver(func(username string) (app.WebhookTarget, bool) {
		exchange, apiID, tradeEnv, ok := bridge.TradeTargetForUser(username)
		if !ok {
			return app.WebhookTarget{}, false
		}
		target := app.WebhookTarget{Exchange: exchange, APIID: apiID, TradeEnv: tradeEnv}
		if token, tokenOK := bridge.WebhookTokenForUser(username, cfg.SteadyWebhookTokenNonce); tokenOK {
			target.Token = token
		}
		return target, true
	})
	bridge.Start(ctx)
	srv, err := httpapi.New(cfg, svc, log, logStore, st)
	if err != nil {
		log.Error("http server configuration failed", "error", err)
		os.Exit(1)
	}
	srv.SetTVBot(bridge.Handler)
	srv.StartCoinGlassScheduler(ctx, cfg.CoinGlassCaptureInterval)
	go func() {
		log.Info("http server listening", "address", cfg.Address)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server failed", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Error("shutdown failed", "error", err)
	}
	log.Info("stopped")
}

func ensureUnifiedAccounts(ctx context.Context, st *store.Store, cfg config.Config) error {
	records, err := st.LoadAuthUserRecords(ctx)
	if err != nil {
		return err
	}
	ensureHash := func(username, hash string) error {
		username, hash = strings.TrimSpace(username), strings.TrimSpace(hash)
		if username == "" || hash == "" {
			return nil
		}
		if current, ok := records[username]; ok {
			if current.PasswordHash != hash {
				return fmt.Errorf("authentication hash conflict for user %q", username)
			}
			if err := st.SetAuthUserState(ctx, username, "admin", false); err != nil {
				return err
			}
			current.Role, current.Disabled = "admin", false
			records[username] = current
			return nil
		}
		if err := st.CreateAuthUser(ctx, username, hash, "admin"); err != nil {
			return err
		}
		records[username] = authn.UserRecord{Username: username, PasswordHash: hash, Role: "admin"}
		return nil
	}
	if err := ensureHash(cfg.AuthUsername, cfg.AuthPasswordHash); err != nil {
		return err
	}
	_, hasTVUser := os.LookupEnv("ADMIN_USER")
	_, hasTVPassword := os.LookupEnv("ADMIN_PASSWORD")
	if hasTVUser || hasTVPassword {
		secrets := tvbotenv.Load()
		username := strings.TrimSpace(secrets.AdminUser)
		if username == "" || secrets.AdminPassword == "" {
			return errors.New("ADMIN_USER and ADMIN_PASSWORD are required for unified account migration")
		}
		if current, ok := records[username]; ok {
			if !authn.VerifyPassword(secrets.AdminPassword, current.PasswordHash) {
				return fmt.Errorf("authentication password conflict for user %q", username)
			}
			if err := st.SetAuthUserState(ctx, username, "admin", false); err != nil {
				return err
			}
		} else {
			hash, err := authn.HashPassword(secrets.AdminPassword)
			if err != nil {
				return err
			}
			if err := st.CreateAuthUser(ctx, username, hash, "admin"); err != nil {
				return err
			}
		}
	}
	return nil
}

func hashPassword() {
	password := readPassword()
	hash, err := authn.HashPassword(password)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not hash password")
		os.Exit(2)
	}
	fmt.Println(hash)
}

func readPassword() string {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
	if err != nil || len(data) > 4096 {
		fmt.Fprintln(os.Stderr, "could not read password")
		os.Exit(2)
	}
	password := strings.TrimRight(string(data), "\r\n")
	if password == "" {
		fmt.Fprintln(os.Stderr, "password must not be empty")
		os.Exit(2)
	}
	return password
}

func addUser(username string) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > 128 {
		fmt.Fprintln(os.Stderr, "username must contain 1 to 128 characters")
		os.Exit(2)
	}
	hash, err := authn.HashPassword(readPassword())
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not hash password")
		os.Exit(2)
	}
	cfg := config.Load()
	st, err := store.Open(cfg.DatabasePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not open authentication database")
		os.Exit(2)
	}
	defer st.Close()
	if err = st.UpsertAuthUser(context.Background(), username, hash); err != nil {
		fmt.Fprintln(os.Stderr, "could not save authentication user")
		os.Exit(2)
	}
	fmt.Printf("authentication user %q saved\n", username)
}
