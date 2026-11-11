package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/alertsmigration"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/app"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/authn"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/config"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/httpapi"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/marketmigration"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/modelsmigration"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/observability"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/optionsmigration"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/settingsmigration"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/store"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotbridge"
	tvbotenv "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotenv"
	tvbotserver "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotserver"
	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/volumemigration"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "hash-password" {
		hashPassword()
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "split-databases" {
		splitDatabases()
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "split-market-database" {
		splitMarketDatabase()
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "split-alerts-database" {
		splitAlertsDatabase()
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "split-options-database" {
		splitOptionsDatabase()
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "split-volume-database" {
		splitVolumeDatabase()
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "split-models-database" {
		splitModelsDatabase()
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "split-settings-database" {
		splitSettingsDatabase()
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
	if migrated, legacyDatabase, err := tvbotbridge.MigrateLegacyDatabase(context.Background(), cfg); err != nil {
		log.Error("legacy tvbot database migration failed", "error", err)
		os.Exit(1)
	} else if migrated {
		log.Info("legacy tvbot database migrated", "source", legacyDatabase)
	}
	marketDatabase := resolveMarketDatabase(context.Background(), cfg, log)
	alertsDatabase := resolveAlertsDatabase(context.Background(), cfg, log)
	optionsDatabase := resolveOptionsDatabase(context.Background(), cfg, log)
	volumeDatabase := resolveVolumeDatabase(context.Background(), cfg, log)
	modelsDatabase := resolveModelsDatabase(context.Background(), cfg, log)
	settingsDatabase := resolveSettingsDatabase(context.Background(), cfg, log)
	st, err := store.OpenWithMarketAndAlertsAndOptionsAndVolumeAndModelsAndSettingsDatabase(cfg.DatabasePath, marketDatabase, alertsDatabase, optionsDatabase, volumeDatabase, modelsDatabase, settingsDatabase)
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
	bridge, err := tvbotbridge.New(cfg, st, log, tvbotserver.BuildInfo{
		CommitTime: httpapi.BuildTime, CommitHash: httpapi.BuildRevision, CommitBranch: httpapi.BuildBranch,
	})
	if err != nil {
		log.Error("initialize trading dashboard failed", "error", err)
		os.Exit(1)
	}
	svc.SetTVBotRetentionPruner(func(ctx context.Context, tradeMonitorBefore, analysisCacheBefore time.Time) (int64, int64, error) {
		return bridge.PruneTVBotRetention(ctx, tradeMonitorBefore, analysisCacheBefore)
	})
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
	svc.SetGammaPolicyResolver(func(username string) string {
		return bridge.GammaNegativePolicyForUser(username)
	})
	svc.SetBackgroundCandidateProvider(bridge)
	bridge.Handler.SetGammaProvider(func(ctx context.Context) tvbotserver.GammaSnapshot {
		gamma := svc.CurrentETHGamma(ctx)
		return tvbotserver.GammaSnapshot{State: gamma.State, Value: gamma.Gamma}
	})
	bridge.Start(ctx)
	svc.Start(ctx)
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
	// Long-lived SSE clients and in-flight exchange requests need a bounded
	// graceful-drain window. Fifteen seconds was shorter than the normal
	// Binance execution timeout and caused avoidable shutdown errors.
	shutdown, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		log.Error("shutdown failed", "error", err)
	}
	log.Info("stopped")
}

func splitDatabases() {
	cfg := config.Load()
	migrated, err := tvbotbridge.SplitDatabases(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not split databases: %v\n", err)
		os.Exit(2)
	}
	if migrated {
		fmt.Println("TVBot database split completed")
	} else {
		fmt.Println("TVBot database split already completed or source database is unavailable")
	}
}

func splitMarketDatabase() {
	cfg := config.Load()
	target := strings.TrimSpace(cfg.MarketDatabasePath)
	if target == "" {
		fmt.Fprintln(os.Stderr, "could not split market database: APP_MARKET_DATABASE is required")
		os.Exit(2)
	}
	sourceAbs, sourceErr := filepath.Abs(cfg.DatabasePath)
	targetAbs, targetErr := filepath.Abs(target)
	if sourceErr != nil || targetErr != nil {
		if sourceErr != nil {
			fmt.Fprintf(os.Stderr, "could not split market database: %v\n", sourceErr)
		} else {
			fmt.Fprintf(os.Stderr, "could not split market database: %v\n", targetErr)
		}
		os.Exit(2)
	}
	if sourceAbs == targetAbs {
		fmt.Fprintln(os.Stderr, "could not split market database: APP_MARKET_DATABASE must differ from APP_DATABASE")
		os.Exit(2)
	}
	migrated, err := marketmigration.MigrateUnifiedSQLite(context.Background(), target, cfg.DatabasePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not split market database: %v\n", err)
		os.Exit(2)
	}
	if migrated {
		fmt.Println("market database split completed")
	} else {
		fmt.Println("market database split already completed or source database is unavailable")
	}
}

func resolveMarketDatabase(ctx context.Context, cfg config.Config, log *slog.Logger) string {
	target := strings.TrimSpace(cfg.MarketDatabasePath)
	if target == "" {
		return ""
	}
	sourceAbs, sourceErr := filepath.Abs(cfg.DatabasePath)
	targetAbs, targetErr := filepath.Abs(target)
	if sourceErr != nil || targetErr != nil || sourceAbs == targetAbs {
		log.Warn("market database path invalid; using unified database", "database", target)
		return ""
	}
	completed, err := marketmigration.HasCompletedMigration(ctx, target, cfg.DatabasePath)
	if err != nil {
		log.Warn("market database migration marker unavailable; using unified database", "database", target, "error", err)
		return ""
	}
	if !completed {
		log.Warn("market database not migrated; using unified database", "database", target)
		return ""
	}
	log.Info("using standalone market database", "database", target)
	return target
}

func splitAlertsDatabase() {
	cfg := config.Load()
	target := strings.TrimSpace(cfg.AlertsDatabasePath)
	if target == "" {
		fmt.Fprintln(os.Stderr, "could not split alerts database: APP_ALERTS_DATABASE is required")
		os.Exit(2)
	}
	sourceAbs, sourceErr := filepath.Abs(cfg.DatabasePath)
	targetAbs, targetErr := filepath.Abs(target)
	if sourceErr != nil || targetErr != nil {
		if sourceErr != nil {
			fmt.Fprintf(os.Stderr, "could not split alerts database: %v\n", sourceErr)
		} else {
			fmt.Fprintf(os.Stderr, "could not split alerts database: %v\n", targetErr)
		}
		os.Exit(2)
	}
	if sourceAbs == targetAbs {
		fmt.Fprintln(os.Stderr, "could not split alerts database: APP_ALERTS_DATABASE must differ from APP_DATABASE")
		os.Exit(2)
	}
	migrated, err := alertsmigration.MigrateUnifiedSQLite(context.Background(), target, cfg.DatabasePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not split alerts database: %v\n", err)
		os.Exit(2)
	}
	if migrated {
		fmt.Println("alerts database split completed")
	} else {
		fmt.Println("alerts database split already completed or source database is unavailable")
	}
}

func resolveAlertsDatabase(ctx context.Context, cfg config.Config, log *slog.Logger) string {
	target := strings.TrimSpace(cfg.AlertsDatabasePath)
	if target == "" {
		return ""
	}
	sourceAbs, sourceErr := filepath.Abs(cfg.DatabasePath)
	targetAbs, targetErr := filepath.Abs(target)
	if sourceErr != nil || targetErr != nil || sourceAbs == targetAbs {
		log.Warn("alerts database path invalid; using unified database", "database", target)
		return ""
	}
	completed, err := alertsmigration.HasCompletedMigration(ctx, target, cfg.DatabasePath)
	if err != nil {
		log.Warn("alerts database migration marker unavailable; using unified database", "database", target, "error", err)
		return ""
	}
	if !completed {
		log.Warn("alerts database not migrated; using unified database", "database", target)
		return ""
	}
	log.Info("using standalone alerts database", "database", target)
	return target
}

func splitOptionsDatabase() {
	cfg := config.Load()
	target := strings.TrimSpace(cfg.OptionsDatabasePath)
	if target == "" {
		fmt.Fprintln(os.Stderr, "could not split options database: APP_OPTIONS_DATABASE is required")
		os.Exit(2)
	}
	sourceAbs, sourceErr := filepath.Abs(cfg.DatabasePath)
	targetAbs, targetErr := filepath.Abs(target)
	if sourceErr != nil || targetErr != nil {
		if sourceErr != nil {
			fmt.Fprintf(os.Stderr, "could not split options database: %v\n", sourceErr)
		} else {
			fmt.Fprintf(os.Stderr, "could not split options database: %v\n", targetErr)
		}
		os.Exit(2)
	}
	if sourceAbs == targetAbs {
		fmt.Fprintln(os.Stderr, "could not split options database: APP_OPTIONS_DATABASE must differ from APP_DATABASE")
		os.Exit(2)
	}
	migrated, err := optionsmigration.MigrateUnifiedSQLite(context.Background(), target, cfg.DatabasePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not split options database: %v\n", err)
		os.Exit(2)
	}
	if migrated {
		fmt.Println("options database split completed")
	} else {
		fmt.Println("options database split already completed or source database is unavailable")
	}
}

func resolveOptionsDatabase(ctx context.Context, cfg config.Config, log *slog.Logger) string {
	target := strings.TrimSpace(cfg.OptionsDatabasePath)
	if target == "" {
		return ""
	}
	sourceAbs, sourceErr := filepath.Abs(cfg.DatabasePath)
	targetAbs, targetErr := filepath.Abs(target)
	if sourceErr != nil || targetErr != nil || sourceAbs == targetAbs {
		log.Warn("options database path invalid; using unified database", "database", target)
		return ""
	}
	completed, err := optionsmigration.HasCompletedMigration(ctx, target, cfg.DatabasePath)
	if err != nil {
		log.Warn("options database migration marker unavailable; using unified database", "database", target, "error", err)
		return ""
	}
	if !completed {
		log.Warn("options database not migrated; using unified database", "database", target)
		return ""
	}
	log.Info("using standalone options database", "database", target)
	return target
}

func splitVolumeDatabase() {
	cfg := config.Load()
	target := strings.TrimSpace(cfg.VolumeDatabasePath)
	if target == "" {
		fmt.Fprintln(os.Stderr, "could not split volume database: APP_VOLUME_DATABASE is required")
		os.Exit(2)
	}
	sourceAbs, sourceErr := filepath.Abs(cfg.DatabasePath)
	targetAbs, targetErr := filepath.Abs(target)
	if sourceErr != nil || targetErr != nil {
		if sourceErr != nil {
			fmt.Fprintf(os.Stderr, "could not split volume database: %v\n", sourceErr)
		} else {
			fmt.Fprintf(os.Stderr, "could not split volume database: %v\n", targetErr)
		}
		os.Exit(2)
	}
	if sourceAbs == targetAbs {
		fmt.Fprintln(os.Stderr, "could not split volume database: APP_VOLUME_DATABASE must differ from APP_DATABASE")
		os.Exit(2)
	}
	migrated, err := volumemigration.MigrateUnifiedSQLite(context.Background(), target, cfg.DatabasePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not split volume database: %v\n", err)
		os.Exit(2)
	}
	if migrated {
		fmt.Println("volume database split completed")
	} else {
		fmt.Println("volume database split already completed or source database is unavailable")
	}
}

func resolveVolumeDatabase(ctx context.Context, cfg config.Config, log *slog.Logger) string {
	target := strings.TrimSpace(cfg.VolumeDatabasePath)
	if target == "" {
		return ""
	}
	sourceAbs, sourceErr := filepath.Abs(cfg.DatabasePath)
	targetAbs, targetErr := filepath.Abs(target)
	if sourceErr != nil || targetErr != nil || sourceAbs == targetAbs {
		log.Warn("volume database path invalid; using unified database", "database", target)
		return ""
	}
	completed, err := volumemigration.HasCompletedMigration(ctx, target, cfg.DatabasePath)
	if err != nil {
		log.Warn("volume database migration marker unavailable; using unified database", "database", target, "error", err)
		return ""
	}
	if !completed {
		log.Warn("volume database not migrated; using unified database", "database", target)
		return ""
	}
	log.Info("using standalone volume database", "database", target)
	return target
}

func splitModelsDatabase() {
	cfg := config.Load()
	target := strings.TrimSpace(cfg.ModelsDatabasePath)
	if target == "" {
		fmt.Fprintln(os.Stderr, "could not split models database: APP_MODELS_DATABASE is required")
		os.Exit(2)
	}
	sourceAbs, sourceErr := filepath.Abs(cfg.DatabasePath)
	targetAbs, targetErr := filepath.Abs(target)
	if sourceErr != nil || targetErr != nil {
		if sourceErr != nil {
			fmt.Fprintf(os.Stderr, "could not split models database: %v\n", sourceErr)
		} else {
			fmt.Fprintf(os.Stderr, "could not split models database: %v\n", targetErr)
		}
		os.Exit(2)
	}
	if sourceAbs == targetAbs {
		fmt.Fprintln(os.Stderr, "could not split models database: APP_MODELS_DATABASE must differ from APP_DATABASE")
		os.Exit(2)
	}
	migrated, err := modelsmigration.MigrateUnifiedSQLite(context.Background(), target, cfg.DatabasePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not split models database: %v\n", err)
		os.Exit(2)
	}
	if migrated {
		fmt.Println("models database split completed")
	} else {
		fmt.Println("models database split already completed or source database is unavailable")
	}
}

func resolveModelsDatabase(ctx context.Context, cfg config.Config, log *slog.Logger) string {
	target := strings.TrimSpace(cfg.ModelsDatabasePath)
	if target == "" {
		return ""
	}
	sourceAbs, sourceErr := filepath.Abs(cfg.DatabasePath)
	targetAbs, targetErr := filepath.Abs(target)
	if sourceErr != nil || targetErr != nil || sourceAbs == targetAbs {
		log.Warn("models database path invalid; using unified database", "database", target)
		return ""
	}
	completed, err := modelsmigration.HasCompletedMigration(ctx, target, cfg.DatabasePath)
	if err != nil {
		log.Warn("models database migration marker unavailable; using unified database", "database", target, "error", err)
		return ""
	}
	if !completed {
		log.Warn("models database not migrated; using unified database", "database", target)
		return ""
	}
	log.Info("using standalone models database", "database", target)
	return target
}

func splitSettingsDatabase() {
	cfg := config.Load()
	target := strings.TrimSpace(cfg.SettingsDatabasePath)
	if target == "" {
		fmt.Fprintln(os.Stderr, "could not split settings database: APP_SETTINGS_DATABASE is required")
		os.Exit(2)
	}
	sourceAbs, sourceErr := filepath.Abs(cfg.DatabasePath)
	targetAbs, targetErr := filepath.Abs(target)
	if sourceErr != nil || targetErr != nil {
		if sourceErr != nil {
			fmt.Fprintf(os.Stderr, "could not split settings database: %v\n", sourceErr)
		} else {
			fmt.Fprintf(os.Stderr, "could not split settings database: %v\n", targetErr)
		}
		os.Exit(2)
	}
	if sourceAbs == targetAbs {
		fmt.Fprintln(os.Stderr, "could not split settings database: APP_SETTINGS_DATABASE must differ from APP_DATABASE")
		os.Exit(2)
	}
	migrated, err := settingsmigration.MigrateUnifiedSQLite(context.Background(), target, cfg.DatabasePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not split settings database: %v\n", err)
		os.Exit(2)
	}
	if migrated {
		fmt.Println("settings database split completed")
	} else {
		fmt.Println("settings database split already completed or source database is unavailable")
	}
}

func resolveSettingsDatabase(ctx context.Context, cfg config.Config, log *slog.Logger) string {
	target := strings.TrimSpace(cfg.SettingsDatabasePath)
	if target == "" {
		return ""
	}
	sourceAbs, sourceErr := filepath.Abs(cfg.DatabasePath)
	targetAbs, targetErr := filepath.Abs(target)
	if sourceErr != nil || targetErr != nil || sourceAbs == targetAbs {
		log.Warn("settings database path invalid; using unified database", "database", target)
		return ""
	}
	completed, err := settingsmigration.HasCompletedMigration(ctx, target, cfg.DatabasePath)
	if err != nil {
		log.Warn("settings database migration marker unavailable; using unified database", "database", target, "error", err)
		return ""
	}
	if !completed {
		log.Warn("settings database not migrated; using unified database", "database", target)
		return ""
	}
	log.Info("using standalone settings database", "database", target)
	return target
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
