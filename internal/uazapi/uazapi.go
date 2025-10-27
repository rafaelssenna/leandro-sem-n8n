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
Pacote de cliente para Uazapi (WhatsApp).

Principais pontos:
- /send/text aceita o campo "delay" (ms) para exibir "Digitando..." antes do envio.
- Mantidas assinaturas compatíveis:
    - SendText(ctx, number, text)                  → envia sem delay
    - SendTextWithDelay(ctx, jidOrNumber, text, delayMs)
- Mídia: SendMediaWithDelay adiciona "delay" se a sua instância suportar no /send/media.
- DownloadByMessageID: baixa mídia via fileURL retornada pelo /message/download.
- HTTP resiliente: até 3 tentativas em falhas transitórias (5xx/timeout/reset).
*/

// Client encapsula chamadas à API Uazapi para envio e download de mídia.
type Client struct {
	baseSend     string
	tokenSend    string
	baseDownload string
	tokenDown    string
	http         *http.Client

	// Opções de resiliência
	maxRetries int
	backoff    time.Duration
}

// New cria um novo cliente Uazapi com URLs base e tokens para envio e download.
func New(baseSend, tokenSend, baseDownload, tokenDown string) *Client {
	return &Client{
		baseSend:     baseSend,
		tokenSend:    tokenSend,
		baseDownload: baseDownload,
		tokenDown:    tokenDown,
		http:         &http.Client{Timeout: 30 * time.Second},
		maxRetries:   3,
		backoff:      250 * time.Millisecond,
	}
}

// WithHTTPClient permite injetar um http.Client customizado.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	if h != nil {
		c.http = h
	}
	return c
}

// WithRetry ajusta tentativas e backoff.
func (c *Client) WithRetry(maxRetries int, backoff time.Duration) *Client {
	if maxRetries >= 0 {
		c.maxRetries = maxRetries
	}
	if backoff > 0 {
		c.backoff = backoff
	}
	return c
}

// ----------------- helpers -----------------

func (c *Client) doJSON(ctx context.Context, url string, token string, body any) (int, []byte, error) {
	buf, _ := json.Marshal(body)

	var lastCode int
	var lastBody []byte
	var lastErr error

	try := 0
	for {
		try++
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("token", token)

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if try <= c.maxRetries && isRetryableNetErr(err) {
				time.Sleep(c.backoff * time.Duration(try))
				continue
			}
			return 0, nil, err
		}

		func() {
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			lastCode, lastBody = resp.StatusCode, b
		}()

		// Sucesso?
		if lastCode >= 200 && lastCode < 300 {
			return lastCode, lastBody, nil
		}

		// Erro 5xx pode ser transitório → retry
		if lastCode >= 500 && lastCode <= 599 && try <= c.maxRetries {
			time.Sleep(c.backoff * time.Duration(try))
			continue
		}

		// Erro 4xx ou excedeu retries
		return lastCode, lastBody, nil
	}
}

func isRetryableNetErr(err error) bool {
	if err == nil {
		return false
	}
	var nerr net.Error
	if errors.As(err, &nerr) {
		// timeout/temporary
		return nerr.Timeout() || nerr.Temporary()
	}
	// conexões resetadas/encerradas
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "unexpected eof")
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

// SendText envia uma mensagem de texto simples no WhatsApp (sem delay).
// Mantido por compatibilidade.
func (c *Client) SendText(ctx context.Context, number, text string) error {
	return c.SendTextWithDelay(ctx, number, text, 0)
}

// SendTextWithDelay envia uma mensagem de texto com suporte ao campo "delay".
// Durante o delay (em ms), o WhatsApp exibirá “Digitando...”.
func (c *Client) SendTextWithDelay(ctx context.Context, jidOrNumber, text string, delayMs int) error {
	number, chatID := makeChatID(jidOrNumber)

	body := map[string]any{
		"number": number,
		"text":   text,
		// Algumas instâncias ainda exigem chatId no payload:
		"chatId": chatID,
		"chatid": chatID,
	}
	if delayMs > 0 {
		body["delay"] = delayMs
	}

	code, b, err := c.doJSON(ctx, c.baseSend+"/send/text", c.tokenSend, body)
	if err != nil {
		return err
	}
	if code > 299 {
		return fmt.Errorf("uazapi send text %d: %s", code, string(b))
	}
	return nil
}

// ----------------- sending (MÍDIA) -----------------

// SendMedia envia um arquivo de mídia (imagem, vídeo, áudio) em base64, sem delay.
func (c *Client) SendMedia(ctx context.Context, number string, mediaType string, data []byte) error {
	return c.SendMediaWithDelay(ctx, number, mediaType, data, 0)
}

// SendMediaWithDelay envia mídia com suporte opcional a "delay" no payload.
// Observação: a maioria das instâncias aceita "delay" também em /send/media.
// Se a sua não aceitar, o servidor apenas ignorará o campo.
func (c *Client) SendMediaWithDelay(ctx context.Context, number string, mediaType string, data []byte, delayMs int) error {
	enc := base64.StdEncoding.EncodeToString(data)
	body := map[string]any{
		"number": number,
		"type":   mediaType,
		"file":   enc,
	}
	if delayMs > 0 {
		body["delay"] = delayMs
	}

	code, b, err := c.doJSON(ctx, c.baseSend+"/send/media", c.tokenSend, body)
	if err != nil {
		return err
	}
	if code > 299 {
		return fmt.Errorf("uazapi send media %d: %s", code, string(b))
	}
	return nil
}

// ----------------- download -----------------

// DownloadByMessageID baixa o conteúdo de uma mensagem via ID.
func (c *Client) DownloadByMessageID(ctx context.Context, messageID string) ([]byte, string, error) {
	body := map[string]any{
		"id":          messageID,
		"return_link": true,
	}

	code, b, err := c.doJSON(ctx, c.baseDownload+"/message/download", c.tokenDown, body)
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

// WaitHumanlike aguarda a duração 'd'. Se typing=true, use a própria API com delay no envio real.
// (Mantido por compatibilidade; prefira chamar diretamente SendTextWithDelay com o delay desejado.)
func (c *Client) WaitHumanlike(ctx context.Context, _ string, d time.Duration, _ bool) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// SendTextAfter agenda um envio de texto com atraso local (utilize delay no payload quando possível).
func (c *Client) SendTextAfter(ctx context.Context, jidOrNumber, text string, d time.Duration, _ bool) error {
	if d > 0 {
		c.WaitHumanlike(ctx, jidOrNumber, d, false)
	}
	// Melhor abordagem: enviar já com delay (server-side typing)
	return c.SendTextWithDelay(ctx, jidOrNumber, text, int(d.Milliseconds()))
}

// SendMediaAfter agenda um envio de mídia com atraso local.
func (c *Client) SendMediaAfter(ctx context.Context, jidOrNumber string, mediaType string, data []byte, d time.Duration, _ bool) error {
	if d > 0 {
		c.WaitHumanlike(ctx, jidOrNumber, d, false)
	}
	return c.SendMediaWithDelay(ctx, onlyDigits(jidOrNumber), mediaType, data, int(d.Milliseconds()))
}
