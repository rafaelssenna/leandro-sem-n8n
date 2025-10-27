package uazapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strings"
	"time"
)

/*
Cliente Uazapi compatível com POST /send/text.

Mostrar “Digitando…” de forma confiável:
- Envia delay (ms) no /send/text (oficial).
- (Opcional) Para delays longos, envia “pulsos” via /wait (ex.: 5500ms + 5500ms …)
  para manter o indicador até o envio do texto.
- Headers: token + convert:true
- Campos extras no payload: readchat, linkPreview=false, typing flags (compat).
*/

type Client struct {
	baseSend     string
	tokenSend    string
	baseDownload string
	tokenDown    string
	http         *http.Client

	maxRetries     int
	backoff        time.Duration
	logReq         bool
	useLegacyWait  bool   // ativa /wait antes do envio
	waitPulseMs    int    // tamanho do pulso em ms (ex.: 5500)
	minVisibleMs   int    // mínimo de delay para garantir visual (ex.: 1000)
	forceTextPaths []string
	forceMediaPaths []string
}

func New(baseSend, tokenSend, baseDownload, tokenDown string) *Client {
	return &Client{
		baseSend:      strings.TrimRight(baseSend, "/"),
		tokenSend:     tokenSend,
		baseDownload:  strings.TrimRight(baseDownload, "/"),
		tokenDown:     tokenDown,
		http:          &http.Client{Timeout: 30 * time.Second},
		maxRetries:    3,
		backoff:       250 * time.Millisecond,
		useLegacyWait: false,
		waitPulseMs:   5500,
		minVisibleMs:  1000,
	}
}

func (c *Client) WithHTTPClient(h *http.Client) *Client {
	if h != nil { c.http = h }
	return c
}
func (c *Client) WithRetry(maxRetries int, backoff time.Duration) *Client {
	if maxRetries >= 0 { c.maxRetries = maxRetries }
	if backoff > 0 { c.backoff = backoff }
	return c
}
func (c *Client) WithLogging(enabled bool) *Client {
	c.logReq = enabled
	return c
}
// Ativa pulsos de /wait para manter “Digitando…” (recomendado quando delay > ~3–4s)
func (c *Client) WithLegacyWait(enabled bool) *Client {
	c.useLegacyWait = enabled
	return c
}
// Ajusta o tamanho do pulso (ms) do /wait (padrão 5500)
func (c *Client) WithWaitPulse(ms int) *Client {
	if ms > 0 { c.waitPulseMs = ms }
	return c
}
func (c *Client) WithMinVisibleDelay(ms int) *Client {
	if ms > 0 { c.minVisibleMs = ms }
	return c
}

// ----------------- helpers -----------------

func joinURL(base, path string) string {
	b := strings.TrimRight(base, "/")
	p := path
	if !strings.HasPrefix(p, "/") { p = "/" + p }
	if strings.HasSuffix(b, "/api") && strings.HasPrefix(p, "/api/") {
		p = strings.TrimPrefix(p, "/api")
		if !strings.HasPrefix(p, "/") { p = "/" + p }
	}
	return b + p
}

func (c *Client) doJSONOnce(ctx context.Context, url string, token string, body any) (int, []byte, error) {
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("token", token)
	req.Header.Set("convert", "true") // muitas instâncias exigem

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
		if code >= 200 && code < 300 { return code, b, nil }
		if code >= 500 && code <= 599 && try <= c.maxRetries {
			time.Sleep(c.backoff * time.Duration(try))
			continue
		}
		return code, b, nil
	}
}

func isRetryableNetErr(err error) bool {
	if err == nil { return false }
	var nerr net.Error
	if errors.As(err, &nerr) { return nerr.Timeout() || nerr.Temporary() }
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
		if ch >= '0' && ch <= '9' { b.WriteByte(ch) }
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

var defaultTextPaths = []string{
	"/send/text",        // docs mostram 'send~text', mas o caminho HTTP real é /send/text
	"/api/send/text",
	"/send-text",
	"/api/send-text",
	"/message/text",
	"/api/message/text",
	"/messages/text",
	"/api/messages/text",
}

// Compat: envia sem delay
func (c *Client) SendText(ctx context.Context, number, text string) error {
	return c.SendTextWithDelay(ctx, number, text, 0)
}

// Envia texto com delay (ms). Para delays longos, pode enviar pulsos /wait.
func (c *Client) SendTextWithDelay(ctx context.Context, jidOrNumber, text string, delayMs int) error {
	number, chatID := makeChatID(jidOrNumber)

	body := map[string]any{
		"number":      number,
		"text":        text,
		"chatId":      chatID,
		"chatid":      chatID,
		"readchat":    true,
		"linkPreview": false,
	}

	// garante um mínimo visível
	if delayMs > 0 {
		if delayMs < c.minVisibleMs { delayMs = c.minVisibleMs }
		body["delay"] = delayMs
		// sinais de compat
		body["typing"] = true
		body["typingTime"] = delayMs
		body["typing_time"] = delayMs
		body["showTyping"] = true
	}

	// --- Pulsos /wait para manter “Digitando…” ativo em delays longos ---
	if c.useLegacyWait && delayMs > 0 {
		if err := c.sendWaitPulsed(ctx, number, chatID, delayMs); err != nil && c.logReq {
			fmt.Println("[uazapi] /wait pulsed error:", err)
		}
		// Após manter o typing com pulsos, mande o texto com delay curto (evita somar atrasos)
		body["delay"] = 300
		body["typingTime"] = 300
		body["typing_time"] = 300
	}

	paths := c.forceTextPaths
	if len(paths) == 0 { paths = defaultTextPaths }

	var lastCode int
	var lastBody []byte
	var lastErr error

	for _, p := range paths {
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

// Mantém “Digitando…” com pulsos /wait (ex.: 5500ms, 5500ms, …) até cobrir delayMs.
func (c *Client) sendWaitPulsed(ctx context.Context, number, chatID string, delayMs int) error {
	pulse := c.waitPulseMs
	if pulse <= 0 { pulse = 5500 }
	remaining := delayMs
	for remaining > 0 {
		step := int(math.Min(float64(remaining), float64(pulse)))
		_ = c.tryWait(ctx, number, chatID, step) // ignorar falhas
		remaining -= step
		// Dá um pequeno respiro entre pulsos longos
		if remaining > 0 && step >= 2000 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return nil
}

// tenta /wait e /send/wait, mas ignora qualquer falha
func (c *Client) tryWait(ctx context.Context, number, chatID string, ms int) error {
	payload := map[string]any{
		"number":   number,
		"chatId":   chatID,
		"chatid":   chatID,
		"ms":       ms,
		"time":     ms,
		"duration": ms,
	}
	// 1) /wait
	url := joinURL(c.baseSend, "/wait")
	if code, _, err := c.doJSONWithRetry(ctx, url, c.tokenSend, payload); err == nil && code < 300 {
		return nil
	}
	// 2) /send/wait
	url2 := joinURL(c.baseSend, "/send/wait")
	if code2, _, err2 := c.doJSONWithRetry(ctx, url2, c.tokenSend, payload); err2 == nil && code2 < 300 {
		return nil
	}
	return nil
}

// ----------------- sending (MÍDIA) -----------------

var defaultMediaPaths = []string{
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
		if delayMs < c.minVisibleMs { delayMs = c.minVisibleMs }
		body["delay"] = delayMs
		body["typing"] = true
		body["typingTime"] = delayMs
		body["typing_time"] = delayMs
		body["showTyping"] = true
	}

	paths := c.forceMediaPaths
	if len(paths) == 0 { paths = defaultMediaPaths }

	var lastCode int
	var lastBody []byte
	var lastErr error
	for _, p := range paths {
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

	var out struct{ FileURL string `json:"fileURL"` }
	if err := json.Unmarshal(b, &out); err != nil { return nil, "", err }
	if out.FileURL == "" { return nil, "", fmt.Errorf("empty fileURL") }

	req2, _ := http.NewRequestWithContext(ctx, http.MethodGet, out.FileURL, nil)
	resp2, err := c.http.Do(req2)
	if err != nil { return nil, out.FileURL, err }
	defer resp2.Body.Close()

	if resp2.StatusCode > 299 {
		b2, _ := io.ReadAll(resp2.Body)
		return nil, out.FileURL, fmt.Errorf("download media %d: %s", resp2.StatusCode, string(b2))
	}
	data, err := io.ReadAll(resp2.Body)
	return data, out.FileURL, err
}

// ----------------- helpers “After” -----------------

// Envia texto já com delay server-side (recomendado)
func (c *Client) SendTextAfter(ctx context.Context, jidOrNumber, text string, d time.Duration, _ bool) error {
	return c.SendTextWithDelay(ctx, jidOrNumber, text, int(d/time.Millisecond))
}
func (c *Client) SendMediaAfter(ctx context.Context, jidOrNumber string, mediaType string, data []byte, d time.Duration, _ bool) error {
	return c.SendMediaWithDelay(ctx, onlyDigits(jidOrNumber), mediaType, data, int(d/time.Millisecond))
}
