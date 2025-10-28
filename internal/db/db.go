package db

import (
    "context"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
)

// Connect creates a new connection pool to Postgres using the provided connection URL.
// It tunes a few defaults for connection count and lifetimes.
func Connect(url string) (*pgxpool.Pool, error) {
    cfg, err := pgxpool.ParseConfig(url)
    if err != nil {
        return nil, err
    }
    // Basic tuning for small workloads
    cfg.MaxConns = 8
    cfg.MinConns = 0
    cfg.MaxConnLifetime = time.Hour
    cfg.MaxConnIdleTime = 10 * time.Minute
    return pgxpool.NewWithConfig(context.Background(), cfg)
}