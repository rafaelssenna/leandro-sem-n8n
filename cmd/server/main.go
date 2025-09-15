package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/your-org/leandro-agent/internal/config"
	"github.com/your-org/leandro-agent/internal/db"
	"github.com/your-org/leandro-agent/internal/handlers"
)

// main is the entrypoint for the WhatsApp assistant server. It loads
// configuration, connects to Postgres, runs auto-migrations, mounts the
// webhook handler and a health endpoint, and starts the HTTP server.
func main() {
	cfg := config.Load()

	// Connect to Postgres
	pool, err := db.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect error: %v", err)
	}
	defer pool.Close()

	// Auto-migrate (creates tables if they don't exist)
	if err := db.AutoMigrate(context.Background(), pool); err != nil {
		log.Fatalf("db migrate error: %v", err)
	}

	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// Webhook for WhatsApp messages. Path is compatible with the original n8n flow.
	mux.Handle("/webhook/Leandro-JW", handlers.NewWebhookHandler(cfg, pool))

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("listening on %s", cfg.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Println("server error:", err)
		os.Exit(1)
	}
}
