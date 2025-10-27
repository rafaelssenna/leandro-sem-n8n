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

	// ajuste o import do pacote conforme a sua árvore:
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

// Cria o cliente Uazapi a partir das ENVs e liga o modo de payload mínimo
// + delay como STRING para gerar exatamente {"number","text","delay":"1000"}.
func newUazapiFromEnv() *uazapi.Client {
	baseSend := getenv("UAZAPI_BASE_SEND", "")
	tokSend := getenv("UAZAPI_TOKEN_SEND", "")
	baseDown := getenv("UAZAPI_BASE_DOWNLOAD", baseSend)
	tokDown := getenv("UAZAPI_TOKEN_DOWNLOAD", tokSend)

	// flags para forçar o payload mínimo e delay como string
	minimalPayload := getenvBool("UAZAPI_MINIMAL_PAYLOAD", true)
	delayAsString := getenvBool("UAZAPI_DELAY_AS_STRING", true)

	cli := uazapi.New(baseSend, tokSend, baseDown, tokDown).
		WithLogging(true).          // logs simples da chamada HTTP (opcional)
		WithMinVisibleDelay(1000) // garante delay >= 1000ms quando informado

	if minimalPayload {
		cli = cli.WithMinimalPayload(true)
	}
	if delayAsString {
		cli = cli.WithDelayAsString(true)
	}

	// IMPORTANTE: nada de /wait neste cliente (versão NO-WAIT no pacote uazapi)
	return cli
}

// main is the entrypoint for the WhatsApp assistant server.
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

	// Instancia o cliente Uazapi com o formato de payload exigido
	uaz := newUazapiFromEnv()

	mux := http.NewServeMux()

	// Health check
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Webhook para WhatsApp.
	// Se o seu handler já aceitar o client, use ESTA linha:
	// mux.Handle("/webhook/Leandro-JW", handlers.NewWebhookHandlerWithUazapi(cfg, pool, uaz))

	// Caso seu handler ainda tenha a assinatura antiga, mantenha esta linha:
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

	_ = uaz // evita “declared and not used” caso você ainda não injete no handler
}
