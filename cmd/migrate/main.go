package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/rakshithyadhav/mrp-go/internal/config"
	"github.com/rakshithyadhav/mrp-go/internal/db"
	
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	cfg := config.Load()
	ctx := context.Background()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("connect database", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	slog.Info("migrations up to date")
}