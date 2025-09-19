package config

import (
	"log"
	"os"
	"strconv"
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
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
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
