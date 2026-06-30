package main

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/sethiramicrosoft/orcastra/internal/api"
	"github.com/sethiramicrosoft/orcastra/internal/db"
	"github.com/sethiramicrosoft/orcastra/internal/deployqueue"
	"github.com/sethiramicrosoft/orcastra/internal/secretcrypto"
)

//go:embed ui
var uiAssets embed.FS

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})

	cfg, err := api.LoadConfigFromEnv()
	if err != nil {
		log.Fatal().Err(err).Msg("invalid configuration")
	}

	pool, err := db.NewPostgresPool(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to connect to database")
	}
	defer pool.Close()

	if err := db.MigrateUp(context.Background(), cfg.DatabaseURL); err != nil {
		log.Fatal().Err(err).Msg("failed to apply database migrations")
	}

	server, err := api.NewServer(cfg, pool)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize API server")
	}

	// Serve embedded frontend with SPA fallback
	uiFS, err := fs.Sub(uiAssets, "ui")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to setup UI filesystem")
	}
	fileServer := http.FileServer(http.FS(uiFS))
	routes := server.RoutesWithUI(fileServer)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddress(),
		Handler:           routes,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Info().Str("addr", cfg.ListenAddress()).Msg("orcastra API listening")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("http server failed")
		}
	}()

	workerCtx, workerCancel := context.WithCancel(context.Background())
	secCipher, err := secretcrypto.New(cfg.EncryptionKeyB64, cfg.EncryptionKeyID, cfg.JWTSecret)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to initialize secret crypto")
	}
	worker := deployqueue.NewWorker(deployqueue.New(pool, secCipher), 2*time.Second)
	go func() {
		if err := worker.Start(workerCtx); err != nil && err != context.Canceled {
			log.Error().Err(err).Msg("deploy worker stopped")
		}
	}()

	shutdownCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-shutdownCtx.Done()
	workerCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("graceful shutdown failed")
	}
	log.Info().Msg("orcastra stopped")
}
