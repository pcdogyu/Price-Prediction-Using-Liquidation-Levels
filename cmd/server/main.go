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
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "hash-password" {
		hashPassword()
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	svc := app.New(cfg, st, log)
	svc.Start(ctx)
	srv, err := httpapi.New(cfg, svc, log, logStore)
	if err != nil {
		log.Error("http server configuration failed", "error", err)
		os.Exit(1)
	}
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

func hashPassword() {
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
	hash, err := authn.HashPassword(password)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not hash password")
		os.Exit(2)
	}
	fmt.Println(hash)
}
