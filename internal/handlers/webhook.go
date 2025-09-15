// internal/handlers/webhook.go
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/your-org/leandro-agent/internal/buffer"
	"github.com/your-org/leandro-agent/internal/config"
	"github.com/your-org/leandro-agent/internal/models"
	"github.com/your-org/leandro-agent/internal/openai"
	"github.com/your-org/leandro-agent/internal/processor"
	"github.com/your-org/leandro-agent/internal/uazapi"
)

type webhookHandler struct {
	cfg    config.Config
	pool   *pgxpool.Pool
	ai     *openai.Client
	wpp    *uazapi.Client
	bufMgr *buffer.Manager
}

func NewWebhookHandler(cfg config.Config, pool *pgxpool.Pool) http.Handler {
	aiClient := openai.New(cfg.OpenAIAPIKey, cfg.OpenAIAssistantID, cfg.OpenAIChatModel, cfg.OpenAITranscribeModel)
	aiClient.TTSVoice = cfg.TTSVoice
	aiClient.TTSSpeed = cfg.TTSSpeed
	wppClient := uazapi.New(cfg.UazapiBaseSend, cfg.UazapiTokenSend, cfg.UazapiBaseDownload, cfg.UazapiTokenDownload)
	h := &webhookHandler{
		cfg:  cfg,
		pool: pool,
		ai:   aiClient,
		wpp:  wppClient,
	}
	h.bufMgr = buffer.NewManager(15*time.Second, func(phone, combined string) {
		go h.processCombinedMessage(context.Background(), phone, combined)
	})
	return h
}

// ===== Estruturas de payload =====

type incomingMessage struct {
	MessageType    string          `json:"messageType"`
	Type           string          `json:"type"`
	Content        json.RawMessage `json:"content"`
	Sender         string          `json:"sender"`
	SenderName     string          `json:"senderName"`
	ChatID         string          `json:"chatid"`
	ChatID2        string          `json:"chatId"`
	MessageID      string          `json:"messageid"`
	MessageID2     string          `json:"messageId"`
	MessageIDAlt   string          `json:"id"`
	ButtonOrListID string          `json:"buttonOrListid"`
	FromMe         bool            `json:"fromMe"`
	WasSentByAPI   bool            `json:"wasSentByApi"`
}

type payloadBody struct{ Message incomingMessage `json:"message"` }
type payloadRoot struct{ Body payloadBody `json:"body"` }

type chatInfo struct {
	WaChatID            string `json:"wa_chatid"`
	WaLastMessageSender string `json:"wa_lastMessageSender"`
}
type eventEnvelope struct {
	Body struct {
		BaseUrl   string          `json:"BaseUrl"`
		EventType string          `json:"EventType"`
		Chat      chatInfo        `json:"chat"`
		Message   incomingMessage `json:"message"`
		Owner     string          `json:"owner"`
		Token     string          `json:"token"`
	} `json:"body"`
}

func (m *incomingMessage) norm() {
	if m.ChatID == "" && m.ChatID2 != "" {
		m.ChatID = m.ChatID2
	}
	if m.MessageID == "" && m.MessageID2 != "" {
		m.MessageID = m.MessageID2
	}
	if m.MessageID == "" && m.MessageIDAlt != "" {
		if i := strings.IndexByte(m.MessageIDAlt, ':'); i >= 0 && i+1 < len(m.MessageIDAlt) {
			m.MessageID = m.MessageIDAlt[i+1:]
		} else {
			m.MessageID = m.MessageIDAlt
		}
	}
}

var chatIDRe = regexp.MustCompile(`^(\d+)(?:@s\.whatsapp\.net|@c\.us|@g\.us|@newsletter)$`)
var anyJIDRe = regexp.MustCompile(`(\d+@(?:s\.whatsapp\.net|c\.us|g\.us|newsletter))`)

func extractPhoneFromJID(jid string) (string, bool) {
	jid = strings.TrimSpace(jid)
	m := chatIDRe.FindStringSubmatch(jid)
	if len(m) == 2 {
		return m[1], true
	}
	return "", false
}

func parsePayload(r *http.Request) (incomingMessage, []byte, error) {
	defer r.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	trimmed := bytes.TrimSpace(raw)

	if len(trimmed) > 0 && trimmed[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(trimmed, &arr); err == nil && len(arr) > 0 {
			raw = arr[0]
			trimmed = bytes.TrimSpace(raw)
		}
	}

	var env eventEnvelope
	if err := json.Unmarshal(trimmed, &env); err == nil {
		msg := env.Body.Message
		msg.norm()
		if msg.ChatID == "" {
			if env.Body.Chat.WaChatID != "" {
				msg.ChatID = env.Body.Chat.WaChatID
			} else if env.Body.Chat.WaLastMessageSender != "" {
				msg.ChatID = env.Body.Chat.WaLastMessageSender
			}
		}
		if msg.ChatID != "" || msg.Sender != "" {
			return msg, raw, nil
		}
	}

	var pr payloadRoot
	if err := json.Unmarshal(trimmed, &pr); err == nil {
		msg := pr.Body.Message
		msg.norm()
		if msg.ChatID != "" || msg.Sender != "" {
			return msg, raw, nil
		}
	}

	var pb payloadBody
	if err := json.Unmarshal(trimmed, &pb); err == nil {
		msg := pb.Message
		msg.norm()
		if msg.ChatID != "" || msg.Sender != "" {
			return msg, raw, nil
		}
	}

	var msg incomingMessage
	if err := json.Unmarshal(trimmed, &msg); err == nil {
		msg.norm()
		if msg.ChatID != "" || msg.Sender != "" {
			return msg, raw, nil
		}
	}

	if m := anyJIDRe.FindStringSubmatch(string(trimmed)); len(m) == 2 {
		msg := incomingMessage{ChatID: m[1]}
		return msg, raw, nil
	}

	return incomingMessage{}, raw, io.EOF
}

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

	if msg.FromMe || msg.WasSentByAPI {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true,"ignored":"fromMe"}`))
		return
	}

	phone, ok := extractPhoneFromJID(msg.ChatID)
	if !ok && msg.Sender != "" {
		phone, ok = extractPhoneFromJID(msg.Sender)
	}
	if !ok {
		if m := anyJIDRe.FindStringSubmatch(string(raw)); len(m) == 2 {
			phone, ok = extractPhoneFromJID(m[1])
		}
	}
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

	textForLLM, _, err := h.normalizeInput(ctx, msg)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "normalize error", err)
		return
	}
	_ = models.InsertMessage(ctx, h.pool, models.Message{
		ClientID: client.ID, Role: "user", Type: "text", Content: textForLLM, ExtID: &msg.MessageID,
	})

	h.bufMgr.AddMessage(phone, textForLLM)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}

func (h *webhookHandler) processCombinedMessage(ctx context.Context, phone string, combined string) {
	client, err := models.GetOrCreateClient(ctx, h.pool, phone, nil)
	if err != nil {
		log.Printf("buffer db error: %v", err)
		return
	}
	threadID := ""
	if client.ThreadID != nil && *client.ThreadID != "" {
		threadID = *client.ThreadID
	} else {
		tid, err := h.ai.CreateThread(ctx)
		if err != nil {
			log.Println("openai thread error:", err)
			return
		}
		if err := models.SetClientThread(ctx, h.pool, client.ID, tid); err != nil {
			log.Println("db set thread error:", err)
			return
		}
		threadID = tid
	}

	_ = models.InsertMessage(ctx, h.pool, models.Message{
		ClientID: client.ID, Role: "user", Type: "text", Content: combined,
	})

	if err := h.ai.AddUserMessage(ctx, threadID, combined); err != nil {
		log.Println("openai add message error:", err)
		return
	}
	runID, err := h.ai.CreateRun(ctx, threadID)
	if err != nil {
		log.Println("openai run error:", err)
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
		log.Println("run not completed:", status)
		return
	}

	reply, err := h.ai.GetLastAssistantText(ctx, threadID)
	if err != nil {
		log.Println("openai get message error:", err)
		return
	}

	_ = models.InsertMessage(ctx, h.pool, models.Message{
		ClientID: client.ID, Role: "assistant", Type: "text", Content: reply,
	})
	if err := h.wpp.SendText(ctx, phone, reply); err != nil {
		log.Println("uazapi send text error:", err)
	}
}

func (h *webhookHandler) normalizeInput(ctx context.Context, msg incomingMessage) (string, string, error) {
	switch strings.ToLower(msg.MessageType) {
	case "extendedtextmessage", "conversation":
		var content string
		_ = json.Unmarshal(msg.Content, &content)
		if content == "" && msg.ButtonOrListID != "" {
			content = msg.ButtonOrListID
		}
		if content == "" {
			content = "(mensagem vazia)"
		}
		return processor.SanitizeText(content), "text", nil

	case "audiomessage", "audio":
		data, _, err := h.wpp.DownloadByMessageID(ctx, msg.MessageID)
		if err != nil {
			return "", "", err
		}
		t, err := h.ai.Transcribe(ctx, data, "audio.ogg")
		if err != nil {
			return "", "", err
		}
		return processor.SanitizeText(t), "audio", nil

	case "imagemessage", "image":
		_, url, err := h.wpp.DownloadByMessageID(ctx, msg.MessageID)
		if err != nil {
			return "", "", err
		}
		desc, err := h.ai.VisionDescribe(ctx, url)
		if err != nil {
			return "", "", err
		}
		return processor.SanitizeText("Descrição da imagem: "+desc), "image", nil

	case "documentmessage", "document":
		data, _, err := h.wpp.DownloadByMessageID(ctx, msg.MessageID)
		if err != nil {
			return "", "", err
		}
		extracted, err := openai.ExtractPDFText(ctx, data)
		if err != nil {
			extracted = "(não foi possível extrair texto do PDF)"
		}
		summary, err := h.ai.SummarizeText(ctx, extracted)
		if err != nil {
			if len(extracted) > 4000 {
				extracted = extracted[:4000]
			}
			return processor.SanitizeText(extracted), "document", nil
		}
		return processor.SanitizeText("Resumo do documento: "+summary), "document", nil

	default:
		var content string
		_ = json.Unmarshal(msg.Content, &content)
		if content == "" {
			content = "(mensagem não suportada: " + msg.MessageType + ")"
		}
		return processor.SanitizeText(content), "text", nil
	}
}
