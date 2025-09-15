package config

import (
    "log"
    "os"
    "strconv"
)

// Config holds all runtime configuration loaded from environment variables.
// See .env.example for a list of expected variables.
type Config struct {
    Addr                  string
    DatabaseURL           string

    OpenAIAPIKey          string
    OpenAIAssistantID     string
    OpenAIChatModel       string
    OpenAITranscribeModel string

    // Uazapi configuration for WhatsApp
    UazapiBaseSend      string
    UazapiTokenSend     string
    UazapiBaseDownload  string
    UazapiTokenDownload string

    // Optional voice settings for TTS
    TTSVoice string
    TTSSpeed float64
}

// getenv returns the value of the given environment variable or def if unset.
func getenv(key, def string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return def
}

// Load reads configuration from environment variables and returns a Config.
// It will log fatal errors for required variables that are missing.
func Load() Config {
    cfg := Config{
        Addr:                  getenv("APP_ADDR", ":8080"),
        DatabaseURL:           os.Getenv("DATABASE_URL"),

        OpenAIAPIKey:          os.Getenv("OPENAI_API_KEY"),
        OpenAIAssistantID:     os.Getenv("OPENAI_ASSISTANT_ID"),
        OpenAIChatModel:       getenv("OPENAI_CHAT_MODEL", "gpt-4o-mini"),
        OpenAITranscribeModel: getenv("OPENAI_TRANSCRIBE_MODEL", "whisper-1"),

        UazapiBaseSend:        os.Getenv("UAZAPI_BASE_SEND"),
        UazapiTokenSend:       os.Getenv("UAZAPI_TOKEN_SEND"),
        UazapiBaseDownload:    getenv("UAZAPI_BASE_DOWNLOAD", os.Getenv("UAZAPI_BASE_SEND")),
        UazapiTokenDownload:   getenv("UAZAPI_TOKEN_DOWNLOAD", os.Getenv("UAZAPI_TOKEN_SEND")),

        TTSVoice:              getenv("TTS_VOICE", "onyx"),
    }
    // Parse optional TTSSpeed
    if s := os.Getenv("TTS_SPEED"); s != "" {
        if f, err := strconv.ParseFloat(s, 64); err == nil {
            cfg.TTSSpeed = f
        }
    }
    if cfg.TTSSpeed == 0 {
        cfg.TTSSpeed = 1.0
    }

    // Validate required fields
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