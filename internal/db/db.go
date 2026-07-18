package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("parse database url : %w", err)
	}

	var pingErr error
	for i := 0; i < 10; i++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		pingErr = pool.Ping(pingCtx)
		cancel()
		if pingErr == nil {
			return pool, nil
		}
		select {
		case <- ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <- time.After(time.Second):
		}
		
	}
	pool.Close()
	return nil, fmt.Errorf("ping db: %w", pingErr)
}