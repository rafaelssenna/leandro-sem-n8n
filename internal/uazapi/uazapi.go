package uazapi

import (
    "bytes"
    "context"
    "encoding/base64"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
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

// SendText sends a plain text WhatsApp message.
func (c *Client) SendText(ctx context.Context, number, text string) error {
    body := map[string]any{"number": number, "text": text}
    buf, _ := json.Marshal(body)
    req, _ := http.NewRequestWithContext(ctx, "POST", c.baseSend+"/send/text", bytes.NewReader(buf))
    req.Header.Set("Accept", "application/json")
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("token", c.tokenSend)
    resp, err := c.http.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode > 299 {
        b, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("uazapi send text %d: %s", resp.StatusCode, string(b))
    }
    return nil
}

// SendMedia sends a media message (e.g., audio) by base64-encoding the content. The mediaType
// should match the type field accepted by Uazapi, e.g. "audio". The data slice is the raw
// bytes of the media file, which will be base64-encoded automatically.
func (c *Client) SendMedia(ctx context.Context, number string, mediaType string, data []byte) error {
    enc := base64.StdEncoding.EncodeToString(data)
    body := map[string]any{
        "number": number,
        "type":   mediaType,
        "file":   enc,
    }
    buf, _ := json.Marshal(body)
    req, _ := http.NewRequestWithContext(ctx, "POST", c.baseSend+"/send/media", bytes.NewReader(buf))
    req.Header.Set("Accept", "application/json")
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("token", c.tokenSend)
    resp, err := c.http.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode > 299 {
        b, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("uazapi send media %d: %s", resp.StatusCode, string(b))
    }
    return nil
}

// DownloadByMessageID requests download info for a message and returns the raw bytes of the file and its URL.
// It posts to /message/download with return_link=true, then follows the returned link.
func (c *Client) DownloadByMessageID(ctx context.Context, messageID string) ([]byte, string, error) {
    body := map[string]any{
        "id":          messageID,
        "return_link": true,
    }
    buf, _ := json.Marshal(body)
    req, _ := http.NewRequestWithContext(ctx, "POST", c.baseDownload+"/message/download", bytes.NewReader(buf))
    req.Header.Set("Accept", "application/json")
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("token", c.tokenDown)
    resp, err := c.http.Do(req)
    if err != nil {
        return nil, "", err
    }
    defer resp.Body.Close()
    if resp.StatusCode > 299 {
        b, _ := io.ReadAll(resp.Body)
        return nil, "", fmt.Errorf("uazapi download %d: %s", resp.StatusCode, string(b))
    }
    var out struct{ FileURL string `json:"fileURL"` }
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
        return nil, "", err
    }
    if out.FileURL == "" {
        return nil, "", fmt.Errorf("empty fileURL")
    }
    // Download the media file
    req2, _ := http.NewRequestWithContext(ctx, "GET", out.FileURL, nil)
    resp2, err := c.http.Do(req2)
    if err != nil {
        return nil, out.FileURL, err
    }
    defer resp2.Body.Close()
    if resp2.StatusCode > 299 {
        b, _ := io.ReadAll(resp2.Body)
        return nil, out.FileURL, fmt.Errorf("download media %d: %s", resp2.StatusCode, string(b))
    }
    data, err := io.ReadAll(resp2.Body)
    return data, out.FileURL, err
}