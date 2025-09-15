package main

import (
    "log"
    "net/http"
    "os"
    "time"

    "github.com/your-org/leandro-agent/internal/config"
    "github.com/your-org/leandro-agent/internal/db"
    "github.com/your-org/leandro-agent/internal/handlers"
)

// main is the entrypoint for the WhatsApp assistant server. It loads
// configuration, connects to Postgres, mounts the webhook handler and a
// simple health endpoint, and starts listening for HTTP requests.
func main() {
    cfg := config.Load()

    // Connect to Postgres
    pool, err := db.Connect(cfg.DatabaseURL)
    if err != nil {
        log.Fatalf("db connect error: %v", err)
    }
    defer pool.Close()

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