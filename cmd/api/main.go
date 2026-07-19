package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rakshithyadhav/mrp-go/internal/config"
	"github.com/rakshithyadhav/mrp-go/internal/db"
	apihttp "github.com/rakshithyadhav/mrp-go/internal/http"
)

func main() {
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("connect db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           apihttp.NewRouter(pool),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		slog.Info("api listening", "addr", server.Addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Server error", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "err", err)
	}
}
