package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/your-org/leandro-agent/internal/config"
	"github.com/your-org/leandro-agent/internal/db"
	"github.com/your-org/leandro-agent/internal/handlers"

	"github.com/your-org/leandro-agent/internal/uazapi"
)

// --- helpers ENV ---
func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return def
}
func getenvBool(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

// Cria o cliente Uazapi a partir das ENVs.
func newUazapiFromEnv() *uazapi.Client {
	baseSend := getenv("UAZAPI_BASE_SEND", "")
	tokSend  := getenv("UAZAPI_TOKEN_SEND", "")
	baseDown := getenv("UAZAPI_BASE_DOWNLOAD", baseSend)
	tokDown  := getenv("UAZAPI_TOKEN_DOWNLOAD", tokSend)

	if baseSend == "" || tokSend == "" {
		log.Fatal("UAZAPI_BASE_SEND e UAZAPI_TOKEN_SEND são obrigatórios")
	}

	minimalPayload := getenvBool("UAZAPI_MINIMAL_PAYLOAD", true)
	delayAsString  := getenvBool("UAZAPI_DELAY_AS_STRING", false) // doc recomenda integer

	cli := uazapi.New(baseSend, tokSend, baseDown, tokDown).
		WithLogging(true).
		WithMinVisibleDelay(1000)

	if minimalPayload {
		cli = cli.WithMinimalPayload(true)
	}
	if delayAsString {
		cli = cli.WithDelayAsString(true)
	}

	return cli
}

func main() {
	cfg := config.Load()

	// DB
	pool, err := db.Connect(cfg.DatabaseURL)
	if err != nil { log.Fatalf("db connect error: %v", err) }
	defer pool.Close()

	if err := db.AutoMigrate(context.Background(), pool); err != nil {
		log.Fatalf("db migrate error: %v", err)
	}

	// Uazapi client (NO-WAIT)
	uaz := newUazapiFromEnv()

	mux := http.NewServeMux()

	// health
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Webhook:
	// RECOMENDADO: injete o client no handler (crie esse construtor no pacote handlers)
	// mux.Handle("/webhook/Leandro-JW", handlers.NewWebhookHandlerWithUazapi(cfg, pool, uaz))

	// Enquanto não tiver o construtor acima, mantém o antigo:
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

	_ = uaz // evita "declared and not used" enquanto não injeta no handler
}
