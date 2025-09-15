package models

import (
    "context"
    "errors"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
)

// Client represents a WhatsApp contact. Each contact can have a thread ID associated
// with the OpenAI assistant. Name is optional. Phone is unique.
type Client struct {
    ID        int64
    Phone     string
    Name      *string
    ThreadID  *string
    CreatedAt time.Time
}

// Message stores each inbound and outbound message exchanged with a client. It helps
// persist conversation history. Role is "user", "assistant", or "system". Type is
// the modality of the content.
type Message struct {
    ID        int64
    ClientID  int64
    Role      string // "user" | "assistant" | "system"
    Type      string // "text" | "audio" | "image" | "document"
    Content   string
    ExtID     *string // messageid from WhatsApp
    CreatedAt time.Time
}

// GetOrCreateClient inserts or retrieves a client row by phone. If the phone
// already exists, it updates the name if previously null. It returns the
// up-to-date Client.
func GetOrCreateClient(ctx context.Context, pool *pgxpool.Pool, phone string, name *string) (Client, error) {
    var c Client
    err := pool.QueryRow(ctx, `
        INSERT INTO clients (phone, name)
        VALUES ($1, $2)
        ON CONFLICT (phone) DO UPDATE SET name = COALESCE(clients.name, EXCLUDED.name)
        RETURNING id, phone, name, thread_id, created_at
    `, phone, name).Scan(&c.ID, &c.Phone, &c.Name, &c.ThreadID, &c.CreatedAt)
    return c, err
}

// SetClientThread sets the thread_id for a given client.
func SetClientThread(ctx context.Context, pool *pgxpool.Pool, clientID int64, threadID string) error {
    ct, err := pool.Exec(ctx, `UPDATE clients SET thread_id=$1 WHERE id=$2`, threadID, clientID)
    if err != nil {
        return err
    }
    if ct.RowsAffected() == 0 {
        return errors.New("client not found")
    }
    return nil
}

// InsertMessage inserts a new message row.
func InsertMessage(ctx context.Context, pool *pgxpool.Pool, m Message) error {
    _, err := pool.Exec(ctx, `
        INSERT INTO messages (client_id, role, type, content, ext_id)
        VALUES ($1,$2,$3,$4,$5)
    `, m.ClientID, m.Role, m.Type, m.Content, m.ExtID)
    return err
}