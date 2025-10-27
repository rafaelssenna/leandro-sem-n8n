package config

import (
	crand "crypto/rand"
	"log"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr        string
	DatabaseURL string

	OpenAIAPIKey          string
	OpenAIAssistantID     string
	OpenAIChatModel       string
	OpenAITranscribeModel string

	UazapiBaseSend      string
	UazapiTokenSend     string
	UazapiBaseDownload  string
	UazapiTokenDownload string

	TTSVoice string
	TTSSpeed float64

	// Timeout do buffer (segundos) via ENV: BUFFER_TIMEOUT_SECONDS
	BufferTimeoutSeconds int

	// ---------- NOVO: Delay antes de responder ----------
	// Delay mínimo/máximo em milissegundos. Se ambos 0, não há atraso.
	ReplyDelayMinMs   int  // ENV: REPLY_DELAY_MIN_MS (ex.: 1500)
	ReplyDelayMaxMs   int  // ENV: REPLY_DELAY_MAX_MS (ex.: 3500)
	TypingDuringDelay bool // ENV: TYPING_DURING_DELAY (true/false). Se true, tenta acionar "digitando..." no provedor.
}

// getenv retorna o valor do env var ou um default.
func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	if n, err := strconv.Atoi(v); err == nil {
		return n
	}
	return def
}

func getenvBool(key string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "t", "yes", "y", "on":
		return true
	case "0", "false", "f", "no", "n", "off":
		return false
	default:
		return def
	}
}

func Load() Config {
	cfg := Config{
		Addr:                  getenv("APP_ADDR", ":8080"),
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		OpenAIAPIKey:          os.Getenv("OPENAI_API_KEY"),
		OpenAIAssistantID:     os.Getenv("OPENAI_ASSISTANT_ID"),
		OpenAIChatModel:       getenv("OPENAI_CHAT_MODEL", "gpt-4o-mini"),
		OpenAITranscribeModel: getenv("OPENAI_TRANSCRIBE_MODEL", "whisper-1"),

		UazapiBaseSend:     os.Getenv("UAZAPI_BASE_SEND"),
		UazapiTokenSend:    os.Getenv("UAZAPI_TOKEN_SEND"),
		UazapiBaseDownload: getenv("UAZAPI_BASE_DOWNLOAD", os.Getenv("UAZAPI_BASE_SEND")),
		UazapiTokenDownload: getenv("UAZAPI_TOKEN_DOWNLOAD",
			os.Getenv("UAZAPI_TOKEN_SEND")),

		TTSVoice: getenv("TTS_VOICE", "onyx"),
	}

	// TTS speed
	if s := os.Getenv("TTS_SPEED"); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			cfg.TTSSpeed = f
		}
	}
	if cfg.TTSSpeed == 0 {
		cfg.TTSSpeed = 1.0
	}

	// Buffer timeout (segundos) — default 15
	if s := getenv("BUFFER_TIMEOUT_SECONDS", "15"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			cfg.BufferTimeoutSeconds = n
		}
	}
	if cfg.BufferTimeoutSeconds == 0 {
		cfg.BufferTimeoutSeconds = 15
	}

	// ---------- NOVO: Delay configurável ----------
	cfg.ReplyDelayMinMs = getenvInt("REPLY_DELAY_MIN_MS", 0)
	cfg.ReplyDelayMaxMs = getenvInt("REPLY_DELAY_MAX_MS", 0)
	cfg.TypingDuringDelay = getenvBool("TYPING_DURING_DELAY", true)

	// Normaliza limites
	if cfg.ReplyDelayMinMs < 0 {
		cfg.ReplyDelayMinMs = 0
	}
	if cfg.ReplyDelayMaxMs < 0 {
		cfg.ReplyDelayMaxMs = 0
	}
	if cfg.ReplyDelayMaxMs > 0 && cfg.ReplyDelayMaxMs < cfg.ReplyDelayMinMs {
		cfg.ReplyDelayMaxMs = cfg.ReplyDelayMinMs
	}

	// Guard rails
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL is required")
	}
	if cfg.OpenAIAPIKey == "" {
		log.Fatal("OPENAI_API_KEY is required")
	}
	if cfg.OpenAIAssistantID == "" {
		log.Fatal("OPENAI_ASSISTANT_ID is required")
	}
	if cfg.UazapiBaseSend == "" || cfg.UazapiTokenSend == "" {
		log.Fatal("UAZAPI_BASE_SEND and UAZAPI_TOKEN_SEND are required")
	}
	return cfg
}

// ReplyDelay retorna a duração de espera antes de responder, aplicando jitter uniforme.
// Se Min/Max forem 0, retorna 0 (sem atraso).
func (c Config) ReplyDelay() time.Duration {
	min := c.ReplyDelayMinMs
	max := c.ReplyDelayMaxMs
	if min <= 0 && max <= 0 {
		return 0
	}
	if max < min {
		max = min
	}
	// sorteio criptograficamente seguro (evita races do math/rand)
	ms := min
	if max > min {
		n, err := crand.Int(crand.Reader, big.NewInt(int64(max-min+1)))
		if err == nil {
			ms = min + int(n.Int64())
		}
	}
	return time.Duration(ms) * time.Millisecond
}
