package uazapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

/*
Cliente Uazapi (WhatsApp) com suporte robusto a "Digitando..." via campo delay.

Diferenciais:
- Envia delay no próprio POST /send/text (oficial).
- Tenta múltiplos caminhos de endpoint: /send/text, /api/send/text, /send-text, /message/text, /messages/text.
- Headers com "convert: true" (muitas instâncias exigem).
- Campos auxiliares: readchat=true, linkPreview=false.
- Delay mínimo de 1000ms (garante visibilidade do status).
- HTTP resiliente (retries e backoff).
*/

type Client struct {
	baseSend     string
	tokenSend    string
	baseDownload string
	tokenDown    string
	http         *http.Client

	maxRetries int
	backoff    time.Duration
	logReq     bool
}

func New(baseSend, tokenSend, baseDownload, tokenDown string) *Client {
	return &Client{
		baseSend:     strings.TrimRight(baseSend, "/"),
		tokenSend:    tokenSend,
		baseDownload: strings.TrimRight(baseDownload, "/"),
		tokenDown:    tokenDown,
		http:         &http.Client{Timeout: 30 * time.Second},
		maxRetries:   3,
		backoff:      250 * time.Millisecond,
	}
}

func (c *Client) WithHTTPClient(h *http.Client) *Client {
	if h != nil {
		c.http = h
	}
	return c
}
func (c *Client) WithRetry(maxRetries int, backoff time.Duration) *Client {
	if maxRetries >= 0 {
		c.maxRetries = maxRetries
	}
	if backoff > 0 {
		c.backoff = backoff
	}
	return c
}
func (c *Client) WithLogging(enabled bool) *Client {
	c.logReq = enabled
	return c
}

// ----------------- helpers -----------------

// tenta (base, path) seguros evitando /api//api
func joinURL(base, path string) string {
	b := strings.TrimRight(base, "/")
	p := path
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if strings.HasSuffix(b, "/api") && strings.HasPrefix(p, "/api/") {
		p = strings.TrimPrefix(p, "/api")
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
	}
	return b + p
}

func (c *Client) doJSONOnce(ctx context.Context, url string, token string, body any) (int, []byte, error) {
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	// MUITAS instâncias exigem estes headers:
	req.Header.Set("token", token)
	req.Header.Set("convert", "true")

	if c.logReq {
		fmt.Printf("[uazapi] POST %s\n", url)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

func (c *Client) doJSONWithRetry(ctx context.Context, url string, token string, body any) (int, []byte, error) {
	var lastCode int
	var lastBody []byte
	var lastErr error

	for try := 1; ; try++ {
		code, b, err := c.doJSONOnce(ctx, url, token, body)
		if err != nil {
			lastErr = err
			if try <= c.maxRetries && isRetryableNetErr(err) {
				time.Sleep(c.backoff * time.Duration(try))
				continue
			}
			return 0, nil, err
		}
		lastCode, lastBody = code, b

		if code >= 200 && code < 300 {
			return code, b, nil
		}
		// 5xx → tentar de novo
		if code >= 500 && code <= 599 && try <= c.maxRetries {
			time.Sleep(c.backoff * time.Duration(try))
			continue
		}
		return code, b, nil
	}
}

func isRetryableNetErr(err error) bool {
	if err == nil {
		return false
	}
	var nerr net.Error
	if errors.As(err, &nerr) {
		return nerr.Timeout() || nerr.Temporary()
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "connection reset") ||
		strings.Contains(s, "connection refused") ||
		strings.Contains(s, "broken pipe") ||
		strings.Contains(s, "unexpected eof")
}

func onlyDigits(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= '0' && ch <= '9' {
			b.WriteByte(ch)
		}
	}
	return b.String()
}

func makeChatID(jidOrNumber string) (number string, chatID string) {
	if strings.Contains(jidOrNumber, "@") {
		return onlyDigits(jidOrNumber), jidOrNumber
	}
	num := onlyDigits(jidOrNumber)
	return num, num + "@s.whatsapp.net"
}

// ----------------- sending (TEXTO) -----------------

// lista de paths prováveis (ordem por “mais comum” primeiro)
var textPaths = []string{
	"/send/text",
	"/api/send/text",
	"/send-text",
	"/api/send-text",
	"/message/text",
	"/api/message/text",
	"/messages/text",
	"/api/messages/text",
}

// SendText: compat, sem delay
func (c *Client) SendText(ctx context.Context, number, text string) error {
	return c.SendTextWithDelay(ctx, number, text, 0)
}

// SendTextWithDelay: envia texto com suporte a "delay" (ms). Durante o delay o WhatsApp exibe “Digitando…”.
func (c *Client) SendTextWithDelay(ctx context.Context, jidOrNumber, text string, delayMs int) error {
	number, chatID := makeChatID(jidOrNumber)

	// muitas instâncias só “mostram” se houver alguns campos padrão
	body := map[string]any{
		"number":      number,
		"text":        text,
		"chatId":      chatID,
		"chatid":      chatID,
		"readchat":    true,
		"linkPreview": false,
	}

	// impõe mínimo de 1000ms para garantir visibilidade
	if delayMs > 0 {
		if delayMs < 1000 {
			delayMs = 1000
		}
		body["delay"] = delayMs
	}

	// tenta em cascata várias rotas
	var lastCode int
	var lastBody []byte
	var lastErr error

	for _, p := range textPaths {
		url := joinURL(c.baseSend, p)
		code, b, err := c.doJSONWithRetry(ctx, url, c.tokenSend, body)
		if err == nil && code >= 200 && code < 300 {
			return nil
		}
		lastCode, lastBody, lastErr = code, b, err
	}

	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("uazapi send text %d: %s", lastCode, string(lastBody))
}

// ----------------- sending (MÍDIA) -----------------

var mediaPaths = []string{
	"/send/media",
	"/api/send/media",
	"/send-media",
	"/api/send-media",
	"/message/media",
	"/api/message/media",
	"/messages/media",
	"/api/messages/media",
}

func (c *Client) SendMedia(ctx context.Context, number string, mediaType string, data []byte) error {
	return c.SendMediaWithDelay(ctx, number, mediaType, data, 0)
}

func (c *Client) SendMediaWithDelay(ctx context.Context, number string, mediaType string, data []byte, delayMs int) error {
	enc := base64.StdEncoding.EncodeToString(data)
	body := map[string]any{
		"number":      onlyDigits(number),
		"type":        mediaType,
		"file":        enc,
		"readchat":    true,
		"linkPreview": false,
	}
	if delayMs > 0 {
		if delayMs < 1000 {
			delayMs = 1000
		}
		body["delay"] = delayMs
	}

	var lastCode int
	var lastBody []byte
	var lastErr error
	for _, p := range mediaPaths {
		url := joinURL(c.baseSend, p)
		code, b, err := c.doJSONWithRetry(ctx, url, c.tokenSend, body)
		if err == nil && code >= 200 && code < 300 {
			return nil
		}
		lastCode, lastBody, lastErr = code, b, err
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("uazapi send media %d: %s", lastCode, string(lastBody))
}

// ----------------- download -----------------

func (c *Client) DownloadByMessageID(ctx context.Context, messageID string) ([]byte, string, error) {
	body := map[string]any{
		"id":          messageID,
		"return_link": true,
	}
	url := joinURL(c.baseDownload, "/message/download")

	code, b, err := c.doJSONWithRetry(ctx, url, c.tokenDown, body)
	if err != nil {
		return nil, "", err
	}
	if code > 299 {
		return nil, "", fmt.Errorf("uazapi download %d: %s", code, string(b))
	}

	var out struct {
		FileURL string `json:"fileURL"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, "", err
	}
	if out.FileURL == "" {
		return nil, "", fmt.Errorf("empty fileURL")
	}

	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, out.FileURL, nil)
	resp2, err := c.http.Do(req2)
	if err != nil {
		return nil, out.FileURL, err
	}
	defer resp2.Body.Close()

	if resp2.StatusCode > 299 {
		b2, _ := io.ReadAll(resp2.Body)
		return nil, out.FileURL, fmt.Errorf("download media %d: %s", resp2.StatusCode, string(b2))
	}

	data, err := io.ReadAll(resp2.Body)
	return data, out.FileURL, err
}

// ----------------- helpers “After” -----------------

// Espera localmente e então envia texto já com delay server-side (garante “Digitando...”).
func (c *Client) SendTextAfter(ctx context.Context, jidOrNumber, text string, d time.Duration, _ bool) error {
	// melhor prática: enviar o delay no próprio payload
	delayMs := int(d / time.Millisecond)
	return c.SendTextWithDelay(ctx, jidOrNumber, text, delayMs)
}

func (c *Client) SendMediaAfter(ctx context.Context, jidOrNumber string, mediaType string, data []byte, d time.Duration, _ bool) error {
	delayMs := int(d / time.Millisecond)
	return c.SendMediaWithDelay(ctx, onlyDigits(jidOrNumber), mediaType, data, delayMs)
}
