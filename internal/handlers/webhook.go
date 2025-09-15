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

type webhookHandler struct {
	cfg  config.Config
	pool *pgxpool.Pool
	ai   *openai.Client
	wpp  *uazapi.Client
}

func NewWebhookHandler(cfg config.Config, pool *pgxpool.Pool) http.Handler {
	aiClient := openai.New(cfg.OpenAIAPIKey, cfg.OpenAIAssistantID, cfg.OpenAIChatModel, cfg.OpenAITranscribeModel)
	aiClient.TTSVoice = cfg.TTSVoice
	aiClient.TTSSpeed = cfg.TTSSpeed
	wppClient := uazapi.New(cfg.UazapiBaseSend, cfg.UazapiTokenSend, cfg.UazapiBaseDownload, cfg.UazapiTokenDownload)
	return &webhookHandler{cfg: cfg, pool: pool, ai: aiClient, wpp: wppClient}
}

// ===== Payloads tolerantes =====

type incomingMessage struct {
	MessageType    string `json:"messageType"`
	Type           string `json:"type"`
	Content        string `json:"content"`
	Sender         string `json:"sender"`
	SenderName     string `json:"senderName"`
	ChatID         string `json:"chatid"`
	ChatID2        string `json:"chatId"`
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

var chatIDRe = regexp.MustCompile(`^(\d+)(?:@s\.whatsapp\.net|@c\.us)$`)

func extractPhone(chatid string) (string, bool) {
	m := chatIDRe.FindStringSubmatch(strings.TrimSpace(chatid))
	if len(m) == 2 {
		return m[1], true
	}
	return "", false
}

func parsePayload(r *http.Request) (incomingMessage, []byte, error) {
	defer r.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))

	// body.message
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
	// message no topo
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
	// objeto plano
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

// helper para padronizar logs de erro + corpo da resposta
func writeErr(w http.ResponseWriter, code int, label string, err error) {
	if err != nil {
		log.Printf("webhook %s: %v", label, err)
		http.Error(w, label+": "+err.Error(), code)
		return
	}
	log.Printf("webhook %s", label)
	http.Error(w, label, code)
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx := r.Context()

	msg, raw, err := parsePayload(r)
	if err != nil {
		log.Printf("webhook invalid json: %s", string(raw))
		writeErr(w, http.StatusBadRequest, "invalid json", nil)
		return
	}

	phone, ok := extractPhone(msg.ChatID)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid chatid: "+msg.ChatID, nil)
		return
	}

	var namePtr *string
	if msg.SenderName != "" {
		namePtr = &msg.SenderName
	}
	client, err := models.GetOrCreateClient(ctx, h.pool, phone, namePtr)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error", err)
		return
	}

	threadID := ""
	if client.ThreadID != nil && *client.ThreadID != "" {
		threadID = *client.ThreadID
	} else {
		tid, err := h.ai.CreateThread(ctx)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "openai thread error", err)
			return
		}
		if err := models.SetClientThread(ctx, h.pool, client.ID, tid); err != nil {
			writeErr(w, http.StatusInternalServerError, "db set thread error", err)
			return
		}
		threadID = tid
	}

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
		writeErr(w, http.StatusInternalServerError, "normalize error", err)
		return
	}

	log.Printf("webhook ok: phone=%s type=%s msgid=%s", phone, msgType, msg.MessageID)

	_ = models.InsertMessage(ctx, h.pool, models.Message{
		ClientID: client.ID, Role: "user", Type: msgType, Content: textForLLM, ExtID: &msg.MessageID,
	})

	if err := h.ai.AddUserMessage(ctx, threadID, textForLLM); err != nil {
		writeErr(w, http.StatusInternalServerError, "openai add message error", err)
		return
	}

	runID, err := h.ai.CreateRun(ctx, threadID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "openai run error", err)
		return
	}

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
		writeErr(w, http.StatusBadGateway, "run not completed: "+status, nil)
		return
	}

	reply, err := h.ai.GetLastAssistantText(ctx, threadID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "openai get message error", err)
		return
	}

	if msgType == "audio" {
		audioBytes, err := h.ai.GenerateSpeech(ctx, reply)
		if err != nil {
			writeErr(w, http.StatusBadGateway, "tts error", err)
			return
		}
		_ = models.InsertMessage(ctx, h.pool, models.Message{ClientID: client.ID, Role: "assistant", Type: "audio", Content: reply})
		if err := h.wpp.SendMedia(ctx, phone, "audio", audioBytes); err != nil {
			writeErr(w, http.StatusBadGateway, "uazapi send audio error", err)
			return
		}
	} else {
		_ = models.InsertMessage(ctx, h.pool, models.Message{ClientID: client.ID, Role: "assistant", Type: "text", Content: reply})
		if err := h.wpp.SendText(ctx, phone, reply); err != nil {
			writeErr(w, http.StatusBadGateway, "uazapi send text error", err)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}

func (h *webhookHandler) normalizeInput(ctx context.Context, msg struct {
	MessageType    string `json:"messageType"`
	Type           string `json:"type"`
	Content        string `json:"content"`
	Sender         string `json:"sender"`
	SenderName     string `json:"senderName"`
	ChatID         string `json:"chatid"`
	MessageID      string `json:"messageid"`
	ButtonOrListID string `
