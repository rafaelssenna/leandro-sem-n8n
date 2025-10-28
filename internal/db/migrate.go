package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// schemaSQL mirrors migrations/001_init.sql
const schemaSQL = `
CREATE TABLE IF NOT EXISTS clients (
  id BIGSERIAL PRIMARY KEY,
  phone TEXT NOT NULL UNIQUE,
  name TEXT NULL,
  thread_id TEXT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_clients_phone ON clients (phone);

CREATE TABLE IF NOT EXISTS messages (
  id BIGSERIAL PRIMARY KEY,
  client_id BIGINT NOT NULL REFERENCES clients(id) ON DELETE CASCADE,
  role TEXT NOT NULL,     -- user | assistant | system
  type TEXT NOT NULL,     -- text | audio | image | document
  content TEXT NOT NULL,
  ext_id TEXT NULL,       -- messageid do WhatsApp
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_messages_client_time ON messages (client_id, created_at DESC);
`

// AutoMigrate applies the schema on startup.
func AutoMigrate(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, schemaSQL)
	return err
}
