package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/your-org/leandro-agent/internal/config"
	"github.com/your-org/leandro-agent/internal/models"
	"github.com/your-org/leandro-agent/internal/openai"
	"github.com/your-org/leandro-agent/internal/processor"
	"github.com/your-org/leandro-agent/internal/uazapi"
)

// webhookHandler processes incoming WhatsApp webhook requests. It normalises
// messages, persists them, forwards the content to OpenAI and responds via
// Uazapi. It maintains a thread per client to keep context.
type webhookHandler struct {
	cfg  config.Config
	pool *pgxpool.Pool
	ai   *openai.Client
	wpp  *uazapi.Client
}

// NewWebhookHandler constructs a new handler with dependencies.
func NewWebhookHandler(cfg config.Config, pool *pgxpool.Pool) http.Handler {
	aiClient := openai.New(cfg.OpenAIAPIKey, cfg.OpenAIAssistantID, cfg.OpenAIChatModel, cfg.OpenAITranscribeModel)
	aiClient.TTSVoice = cfg.TTSVoice
	aiClient.TTSSpeed = cfg.TTSSpeed
	wppClient := uazapi.New(cfg.UazapiBaseSend, cfg.UazapiTokenSend, cfg.UazapiBaseDownload, cfg.UazapiTokenDownload)
	return &webhookHandler{
		cfg:  cfg,
		pool: pool,
		ai:   aiClient,
		wpp:  wppClient,
	}
}

// ===== Payloads tolerantes =====

// incomingMessage aceita variações comuns de campos
type incomingMessage struct {
	MessageType    string `json:"messageType"`
	Type           string `json:"type"`
	Content        string `json:"content"`
	Sender         string `json:"sender"`
	SenderName     string `json:"senderName"`
	ChatID         string `json:"chatid"`   // minúsculas
	ChatID2        string `json:"chatId"`   // CamelCase
	MessageID      string `json:"messageid"`
	MessageID2     string `json:"messageId"`
	ButtonOrListID string `json:"buttonOrListid"`
}

type payloadBody struct {
	Message incomingMessage `json:"message"`
}

type payloadRoot struct {
	Body payloadBody `json:"body"`
}

func (m *incomingMessage) norm() {
	if m.ChatID == "" && m.ChatID2 != "" {
		m.ChatID = m.ChatID2
	}
	if m.MessageID == "" && m.MessageID2 != "" {
		m.MessageID = m.MessageID2
	}
}

// Chat ID format: digits@c.us or digits@s.whatsapp.net. Extract only digits.
var chatIDRe = regexp.MustCompile(`^(\d+)(?:@s\.whatsapp\.net|@c\.us)$`)

// extractPhone extracts the numeric phone from chatID.
func extractPhone(chatid string) (string, bool) {
	m := chatIDRe.FindStringSubmatch(strings.TrimSpace(chatid))
	if len(m) == 2 {
		return m[1], true
	}
	return "", false
}

// parsePayload aceita body.message, message no topo ou objeto plano
func parsePayload(r *http.Request) (incomingMessage, []byte, error) {
	defer r.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20)) // 2MB

	// 1) tenta body.message
	{
		var pr payloadRoot
		if err := json.Unmarshal(raw, &pr); err == nil {
			msg := pr.Body.Message
			msg.norm()
			if msg.ChatID != "" || msg.ChatID2 != "" || msg.Sender != "" {
				return msg, raw, nil
			}
		}
	}
	// 2) tenta message no topo
	{
		var pb payloadBody
		if err := json.Unmarshal(raw, &pb); err == nil {
			msg := pb.Message
			msg.norm()
			if msg.ChatID != "" || msg.ChatID2 != "" || msg.Sender != "" {
				return msg, raw, nil
			}
		}
	}
	// 3) objeto plano
	{
		var msg incomingMessage
		if err := json.Unmarshal(raw, &msg); err == nil {
			msg.norm()
			if msg.ChatID != "" || msg.ChatID2 != "" || msg.Sender != "" {
				return msg, raw, nil
			}
		}
	}

	return incomingMessage{}, raw, io.EOF
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()

	// Parse tolerante e log do payload bruto quando inválido
	msg, raw, err := parsePayload(r)
	if err != nil {
		log.Printf("webhook invalid json: %s", string(raw))
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	phone, ok := extractPhone(msg.ChatID)
	if !ok {
		http.Error(w, "invalid chatid: "+msg.ChatID, http.StatusBadRequest)
		return
	}

	// Upsert client
	var namePtr *string
	if msg.SenderName != "" {
		namePtr = &msg.SenderName
	}
	client, err := models.GetOrCreateClient(ctx, h.pool, phone, namePtr)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	// Ensure thread exists
	threadID := ""
	if client.ThreadID != nil && *client.ThreadID != "" {
		threadID = *client.ThreadID
	} else {
		tid, err := h.ai.CreateThread(ctx)
		if err != nil {
			http.Error(w, "openai thread error", http.StatusInternalServerError)
			return
		}
		if err := models.SetClientThread(ctx, h.pool, client.ID, tid); err != nil {
			http.Error(w, "db set thread error", http.StatusInternalServerError)
			return
		}
		threadID = tid
	}

	// Normalise inbound message: get text for LLM and detect input type
	textForLLM, msgType, err := h.normalizeInput(ctx, struct {
		MessageType    string `json:"messageType"`
		Type           string `json:"type"`
		Content        string `json:"content"`
		Sender         string `json:"sender"`
		SenderName     string `json:"senderName"`
		ChatID         string `json:"chatid"`
		MessageID      string `json:"messageid"`
		ButtonOrListID string `json:"buttonOrListid"`
	}{
		MessageType:    msg.MessageType,
		Type:           msg.Type,
		Content:        msg.Content,
		Sender:         msg.Sender,
		SenderName:     msg.SenderName,
		ChatID:         msg.ChatID,
		MessageID:      msg.MessageID,
		ButtonOrListID: msg.ButtonOrListID,
	})
	if err != nil {
		http.Error(w, "normalize error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Persist inbound message
	_ = models.InsertMessage(ctx, h.pool, models.Message{
		ClientID: client.ID, Role: "user", Type: msgType, Content: textForLLM, ExtID: &msg.MessageID,
	})

	// Add message to thread
	if err := h.ai.AddUserMessage(ctx, threadID, textForLLM); err != nil {
		http.Error(w, "openai add message error", http.StatusInternalServerError)
		return
	}
	// Create run
	runID, err := h.ai.CreateRun(ctx, threadID)
	if err != nil {
		http.Error(w, "openai run error", http.StatusInternalServerError)
		return
	}
	// Poll until run completes or fails (up to ~20 seconds)
	status := ""
	for i := 0; i < 10; i++ {
		time.Sleep(2 * time.Second)
		status, err = h.ai.GetRun(ctx, threadID, runID)
		if err != nil {
			break
		}
		if status == "completed" || status == "failed" || status == "expired" {
			break
		}
	}
	if status != "completed" {
		http.Error(w, "run not completed: "+status, http.StatusBadGateway)
		return
	}
	// Fetch assistant reply
	reply, err := h.ai.GetLastAssistantText(ctx, threadID)
	if err != nil {
		http.Error(w, "openai get message error", http.StatusInternalServerError)
		return
	}
	// Persist assistant response as text or audio depending on input type
	if msgType == "audio" {
		// Generate TTS audio and send media
		audioBytes, err := h.ai.GenerateSpeech(ctx, reply)
		if err != nil {
			http.Error(w, "tts error: "+err.Error(), http.StatusBadGateway)
			return
		}
		_ = models.InsertMessage(ctx, h.pool, models.Message{
			ClientID: client.ID, Role: "assistant", Type: "audio", Content: reply,
		})
		if err := h.wpp.SendMedia(ctx, phone, "audio", audioBytes); err != nil {
			http.Error(w, "uazapi send audio error: "+err.Error(), http.StatusBadGateway)
			return
		}
	} else {
		_ = models.InsertMessage(ctx, h.pool, models.Message{
			ClientID: client.ID, Role: "assistant", Type: "text", Content: reply,
		})
		if err := h.wpp.SendText(ctx, phone, reply); err != nil {
			http.Error(w, "uazapi send text error: "+err.Error(), http.StatusBadGateway)
			return
		}
	}
	// Success
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}

// normalizeInput converts incoming WhatsApp message types into a plain text string
// suitable for the LLM and returns the derived modality. It handles text,
// audio, image and document messages.
func (h *webhookHandler) normalizeInput(ctx context.Context, msg struct {
	MessageType    string `json:"messageType"`
	Type           string `json:"type"`
	Content        string `json:"content"`
	Sender         string `json:"sender"`
	SenderName     string `json:"senderName"`
	ChatID         string `json:"chatid"`
	MessageID      string `json:"messageid"`
	ButtonOrListID string `json:"buttonOrListid"`
}) (string, string, error) {
	switch msg.MessageType {
	case "ExtendedTextMessage", "Conversation":
		// plain text or interactive
		content := msg.Content
		if content == "" && msg.ButtonOrListID != "" {
			content = msg.ButtonOrListID
		}
		if content == "" {
			content = "(mensagem vazia)"
		}
		return processor.SanitizeText(content), "text", nil
	case "AudioMessage":
		// download audio and transcribe
		data, _, err := h.wpp.DownloadByMessageID(ctx, msg.MessageID)
		if err != nil {
			return "", "", err
		}
		t, err := h.ai.Transcribe(ctx, data, "audio.ogg")
		if err != nil {
			return "", "", err
		}
		return processor.SanitizeText(t), "audio", nil
	case "ImageMessage":
		// download image and describe via vision
		_, url, err := h.wpp.DownloadByMessageID(ctx, msg.MessageID)
		if err != nil {
			return "", "", err
		}
		desc, err := h.ai.VisionDescribe(ctx, url)
		if err != nil {
			return "", "", err
		}
		return processor.SanitizeText("Descrição da imagem: " + desc), "image", nil
	case "DocumentMessage":
		// download document (likely PDF), extract text and summarise
		data, _, err := h.wpp.DownloadByMessageID(ctx, msg.MessageID)
		if err != nil {
			return "", "", err
		}
		// Attempt to extract PDF text
		extracted, err := openai.ExtractPDFText(ctx, data)
		if err != nil {
			// fallback: treat as unsupported
			extracted = "(não foi possível extrair texto do PDF)"
		}
		// Summarise large documents before sending to LLM
		summary, err := h.ai.SummarizeText(ctx, extracted)
		if err != nil {
			// If summarisation fails, just truncate the extracted text
			if len(extracted) > 4000 {
				extracted = extracted[:4000]
			}
			return processor.SanitizeText(extracted), "document", nil
		}
		return processor.SanitizeText("Resumo do documento: " + summary), "document", nil
	default:
		// fallback to text using content
		content := msg.Content
		if content == "" {
			content = "(mensagem não suportada: " + msg.MessageType + ")"
		}
		return processor.SanitizeText(content), "text", nil
	}
}
