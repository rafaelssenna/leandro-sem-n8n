package uazapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client encapsulates calls to the Uazapi WhatsApp API for sending and downloading media.
type Client struct {
	baseSend     string
	tokenSend    string
	baseDownload string
	tokenDown    string
	http         *http.Client
}

// New creates a new Uazapi client given base URLs and tokens for sending and downloading.
func New(baseSend, tokenSend, baseDownload, tokenDown string) *Client {
	return &Client{
		baseSend:     baseSend,
		tokenSend:    tokenSend,
		baseDownload: baseDownload,
		tokenDown:    tokenDown,
		http:         &http.Client{},
	}
}

// ----------------- helpers -----------------

func (c *Client) doJSON(ctx context.Context, url string, token string, body any) (int, []byte, error) {
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(buf))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("token", token)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
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
		// já é JID
		return onlyDigits(jidOrNumber), jidOrNumber
	}
	num := onlyDigits(jidOrNumber)
	return num, num + "@s.whatsapp.net"
}

// ----------------- sending -----------------

// SendText sends a plain text WhatsApp message.
// Mantido por compatibilidade; agora ele usa o método novo sem delay.
func (c *Client) SendText(ctx context.Context, number, text string) error {
	return c.SendTextWithDelay(ctx, number, text, 0, false)
}

// SendTextWithDelay envia texto e, se delayMs > 0, tenta exibir "digitando..."
// e adicionar os campos de delay no próprio payload do /send/text.
//
// - jidOrNumber: pode ser número ou JID; ambos são aceitos.
// - delayMs: atraso desejado em milissegundos (mostra "digitando..." esse tempo).
// - alsoSendWait: se true, faz uma chamada separada a /wait (fallback) antes de enviar.
//   Mesmo enviando /wait, mantemos os campos no payload para cobrir instâncias que só respeitam o delay via body.
func (c *Client) SendTextWithDelay(ctx context.Context, jidOrNumber, text string, delayMs int, alsoSendWait bool) error {
	number, chatID := makeChatID(jidOrNumber)

	// Monta payload amplo para cobrir variações de instância:
	body := map[string]any{
		"number": number,
		"text":   text,

		// Algumas instâncias requerem chatId além de number
		"chatId": chatID,
		"chatid": chatID,
	}

	if delayMs > 0 {
		// Opcionalmente, aciona "digitando..." via endpoint específico (se existir na instância)
		if alsoSendWait {
			_ = c.SendWait(ctx, jidOrNumber, delayMs) // ignoramos erro de propósito
		}

		// Muitos backends Uazapi-like entendem um ou mais destes campos:
		body["typing"] = true
		body["typingTime"] = delayMs   // variante comum
		body["typing_time"] = delayMs  // outra variante
		body["delay"] = delayMs        // alguns usam "delay"
		body["delayMs"] = delayMs      // outros "delayMs"
		body["time"] = delayMs         // alguns "time"
		body["ms"] = delayMs           // alguns "ms"
		body["duration"] = delayMs     // outros "duration"
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

// SendMedia sends a media message (e.g., audio) by base64-encoding the content.
func (c *Client) SendMedia(ctx context.Context, number string, mediaType string, data []byte) error {
	enc := base64.StdEncoding.EncodeToString(data)
	body := map[string]any{
		"number": number,
		"type":   mediaType,
		"file":   enc,
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

// DownloadByMessageID requests download info for a message and returns the raw bytes and its URL.
func (c *Client) DownloadByMessageID(ctx context.Context, messageID string) ([]byte, string, error) {
	body := map[string]any{"id": messageID, "return_link": true}
	code, b, err := c.doJSON(ctx, c.baseDownload+"/message/download", c.tokenDown, body)
	if err != nil {
		return nil, "", err
	}
	if code > 299 {
		return nil, "", fmt.Errorf("uazapi download %d: %s", code, string(b))
	}
	var out struct{ FileURL string `json:"fileURL"` }
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, "", err
	}
	if out.FileURL == "" {
		return nil, "", fmt.Errorf("empty fileURL")
	}

	req2, _ := http.NewRequestWithContext(ctx, "GET", out.FileURL, nil)
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

// SendWait mostra o indicador "digitando..." por 'ms' milissegundos.
// Compatível com variantes da API (tenta /wait e fallback /send/wait; envia number e chatId).
func (c *Client) SendWait(ctx context.Context, jidOrNumber string, ms int) error {
	if ms <= 0 {
		return nil
	}
	number, chatID := makeChatID(jidOrNumber)

	// Envia chaves amplas para cobrir variações da API
	payload := map[string]any{
		"number":   number,
		"chatId":   chatID,
		"chatid":   chatID,
		"ms":       ms,
		"time":     ms,
		"duration": ms,
	}

	// Tentativa 1: /wait
	if code, b, err := c.doJSON(ctx, c.baseSend+"/wait", c.tokenSend, payload); err == nil && code < 300 {
		return nil
	} else if err == nil && (code == 404 || code == 405) {
		// Fallback 2: /send/wait
		if code2, b2, err2 := c.doJSON(ctx, c.baseSend+"/send/wait", c.tokenSend, payload); err2 == nil && code2 < 300 {
			return nil
		} else if err2 == nil {
			return fmt.Errorf("uazapi wait %d: %s", code2, string(b2))
		} else {
			return err2
		}
	} else if err == nil {
		return fmt.Errorf("uazapi wait %d: %s", code, string(b))
	} else {
		return err
	}
}

// ----------------- helpers de delay (opcionais) -----------------

// WaitHumanlike aguarda a duração 'd'. Se typing=true, tenta acionar "digitando..." durante a espera.
func (c *Client) WaitHumanlike(ctx context.Context, jidOrNumber string, d time.Duration, typing bool) {
	ms := int(d / time.Millisecond)
	if ms <= 0 {
		return
	}
	if typing {
		// Ignora erro propositalmente para não quebrar o fluxo se o endpoint não existir.
		_ = c.SendWait(ctx, jidOrNumber, ms)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		return
	}
}

// SendTextAfter aguarda 'd' (opcionalmente exibindo "digitando...") e então envia o texto.
// Este método faz a espera no cliente e não depende de o backend aceitar "delay" no body.
func (c *Client) SendTextAfter(ctx context.Context, jidOrNumber, text string, d time.Duration, typing bool) error {
	c.WaitHumanlike(ctx, jidOrNumber, d, typing)
	return c.SendTextWithDelay(ctx, jidOrNumber, text, 0, false)
}

// SendMediaAfter aguarda 'd' (opcionalmente exibindo "digitando...") e então envia a mídia.
func (c *Client) SendMediaAfter(ctx context.Context, jidOrNumber string, mediaType string, data []byte, d time.Duration, typing bool) error {
	c.WaitHumanlike(ctx, jidOrNumber, d, typing)
	return c.SendMedia(ctx, onlyDigits(jidOrNumber), mediaType, data)
}
