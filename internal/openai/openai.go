package openai

import (
    "bytes"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "mime/multipart"
    "net/http"
    "os"
    "os/exec"
    "strings"
    "time"
)

// Client wraps HTTP calls to the OpenAI API for assistants, vision, transcribe,
// summarisation and text-to-speech. Fields like TTSVoice and TTSSpeed can be
// configured via Config.
type Client struct {
    apiKey          string
    assistantID     string
    chatModel       string
    transcribeModel string
    http            *http.Client

    TTSVoice string
    TTSSpeed float64
}

// New returns a new Client. Caller should set TTSVoice and TTSSpeed on the
// returned instance if they differ from defaults.
func New(apiKey, assistantID, chatModel, transcribeModel string) *Client {
    return &Client{
        apiKey:          apiKey,
        assistantID:     assistantID,
        chatModel:       chatModel,
        transcribeModel: transcribeModel,
        http:            &http.Client{Timeout: 60 * time.Second},
        TTSVoice:        "onyx",
        TTSSpeed:        1.0,
    }
}

// do sends the HTTP request with authentication header. The caller must set
// appropriate Content-Type if not JSON.
func (c *Client) do(req *http.Request) (*http.Response, error) {
    req.Header.Set("Authorization", "Bearer "+c.apiKey)
    return c.http.Do(req)
}

// CreateThread creates a new empty thread for assistants v2.
func (c *Client) CreateThread(ctx context.Context) (string, error) {
    req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/threads", bytes.NewReader([]byte(`{}`)))
    req.Header.Set("OpenAI-Beta", "assistants=v2")
    req.Header.Set("Content-Type", "application/json")
    resp, err := c.do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    if resp.StatusCode > 299 {
        b, _ := io.ReadAll(resp.Body)
        return "", fmt.Errorf("create thread status %d: %s", resp.StatusCode, string(b))
    }
    var tr struct{ ID string `json:"id"` }
    if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
        return "", err
    }
    return tr.ID, nil
}

// AddUserMessage appends a user message with plain text to a thread.
func (c *Client) AddUserMessage(ctx context.Context, threadID string, text string) error {
    body := map[string]any{
        "role":    "user",
        "content": []map[string]string{{"type": "text", "text": text}},
    }
    buf, _ := json.Marshal(body)
    u := fmt.Sprintf("https://api.openai.com/v1/threads/%s/messages", threadID)
    req, _ := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(buf))
    req.Header.Set("OpenAI-Beta", "assistants=v2")
    req.Header.Set("Content-Type", "application/json")
    resp, err := c.do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode > 299 {
        b, _ := io.ReadAll(resp.Body)
        return fmt.Errorf("add message status %d: %s", resp.StatusCode, string(b))
    }
    return nil
}

// CreateRun creates a run for a given thread.
func (c *Client) CreateRun(ctx context.Context, threadID string) (string, error) {
    body := map[string]any{ "assistant_id": c.assistantID }
    buf, _ := json.Marshal(body)
    u := fmt.Sprintf("https://api.openai.com/v1/threads/%s/runs", threadID)
    req, _ := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(buf))
    req.Header.Set("OpenAI-Beta", "assistants=v2")
    req.Header.Set("Content-Type", "application/json")
    resp, err := c.do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    if resp.StatusCode > 299 {
        b, _ := io.ReadAll(resp.Body)
        return "", fmt.Errorf("create run status %d: %s", resp.StatusCode, string(b))
    }
    var rr struct{ ID string `json:"id"`; Status string `json:"status"` }
    if err := json.NewDecoder(resp.Body).Decode(&rr); err != nil {
        return "", err
    }
    return rr.ID, nil
}

// GetRun returns the run status.
func (c *Client) GetRun(ctx context.Context, threadID, runID string) (string, error) {
    u := fmt.Sprintf("https://api.openai.com/v1/threads/%s/runs/%s", threadID, runID)
    req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
    req.Header.Set("OpenAI-Beta", "assistants=v2")
    resp, err := c.do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    if resp.StatusCode > 299 {
        b, _ := io.ReadAll(resp.Body)
        return "", fmt.Errorf("get run status %d: %s", resp.StatusCode, string(b))
    }
    var rs struct{ ID string `json:"id"`; Status string `json:"status"` }
    if err := json.NewDecoder(resp.Body).Decode(&rs); err != nil {
        return "", err
    }
    return rs.Status, nil
}

// GetLastAssistantText fetches the most recent assistant message text from a thread.
func (c *Client) GetLastAssistantText(ctx context.Context, threadID string) (string, error) {
    u := fmt.Sprintf("https://api.openai.com/v1/threads/%s/messages?order=desc&limit=1", threadID)
    req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
    req.Header.Set("OpenAI-Beta", "assistants=v2")
    resp, err := c.do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    if resp.StatusCode > 299 {
        b, _ := io.ReadAll(resp.Body)
        return "", fmt.Errorf("list messages status %d: %s", resp.StatusCode, string(b))
    }
    var lm struct {
        Data []struct{
            Content []struct{
                Type string `json:"type"`
                Text *struct{ Value string `json:"value"` } `json:"text,omitempty"`
            } `json:"content"`
        } `json:"data"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&lm); err != nil {
        return "", err
    }
    if len(lm.Data) == 0 || len(lm.Data[0].Content) == 0 || lm.Data[0].Content[0].Text == nil {
        return "", errors.New("no assistant text found")
    }
    return lm.Data[0].Content[0].Text.Value, nil
}

// VisionDescribe calls chat completions with an image URL to generate a description.
func (c *Client) VisionDescribe(ctx context.Context, imageURL string) (string, error) {
    body := map[string]any{
        "model": c.chatModel,
        "messages": []any{
            map[string]any{
                "role": "user",
                "content": []any{
                    map[string]string{"type": "text", "text": "Analise e descreva objetivamente a imagem:"},
                    map[string]any{"type": "image_url", "image_url": map[string]string{"url": imageURL}},
                },
            },
        },
        "max_tokens": 400,
    }
    buf, _ := json.Marshal(body)
    req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(buf))
    req.Header.Set("Content-Type", "application/json")
    resp, err := c.do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    if resp.StatusCode > 299 {
        b, _ := io.ReadAll(resp.Body)
        return "", fmt.Errorf("vision status %d: %s", resp.StatusCode, string(b))
    }
    var out struct {
        Choices []struct{ Message struct{ Content string `json:"content"` } `json:"message"` } `json:"choices"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
        return "", err
    }
    if len(out.Choices) == 0 {
        return "", errors.New("no vision choice")
    }
    return out.Choices[0].Message.Content, nil
}

// Transcribe uploads audio bytes to OpenAI and returns the transcribed text.
func (c *Client) Transcribe(ctx context.Context, audio []byte, filename string) (string, error) {
    var b bytes.Buffer
    w := multipart.NewWriter(&b)
    _ = w.WriteField("model", c.transcribeModel)
    fw, err := w.CreateFormFile("file", filename)
    if err != nil {
        return "", err
    }
    if _, err := fw.Write(audio); err != nil {
        return "", err
    }
    w.Close()

    req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/audio/transcriptions", &b)
    req.Header.Set("Authorization", "Bearer "+c.apiKey)
    req.Header.Set("Content-Type", w.FormDataContentType())
    resp, err := c.http.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close()
    if resp.StatusCode > 299 {
        bb, _ := io.ReadAll(resp.Body)
        return "", fmt.Errorf("transcribe status %d: %s", resp.StatusCode, string(bb))
    }
    var out struct{ Text string `json:"text"` }
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
        return "", err
    }
    return out.Text, nil
}

// GenerateSpeech uses the OpenAI TTS endpoint to convert text to speech.
// It returns the raw audio bytes (mp3 by default).
func (c *Client) GenerateSpeech(ctx context.Context, text string) ([]byte, error) {
    body := map[string]any{
        "model":           "tts-1",
        "input":           text,
        "voice":           c.TTSVoice,
        "speed":           c.TTSSpeed,
        "response_format": "mp3",
    }
    buf, _ := json.Marshal(body)
    req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/audio/speech", bytes.NewReader(buf))
    req.Header.Set("Authorization", "Bearer "+c.apiKey)
    req.Header.Set("Content-Type", "application/json")
    resp, err := c.http.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    if resp.StatusCode > 299 {
        b, _ := io.ReadAll(resp.Body)
        return nil, fmt.Errorf("tts status %d: %s", resp.StatusCode, string(b))
    }
    data, err := io.ReadAll(resp.Body)
    return data, err
}

// SummarizeText uses chat completions to summarise a large chunk of text. If the call fails,
// it returns the original text truncated.
func (c *Client) SummarizeText(ctx context.Context, text string) (string, error) {
    const maxInputLen = 12000
    if len(text) > maxInputLen {
        text = text[:maxInputLen]
    }
    body := map[string]any{
        "model": c.chatModel,
        "messages": []any{
            map[string]string{
                "role":    "system",
                "content": "Você é um assistente que resume documentos. Resuma o texto fornecido de forma concisa, mantendo as ideias principais. Responda em Português.",
            },
            map[string]string{
                "role":    "user",
                "content": text,
            },
        },
        "max_tokens":  512,
        "temperature": 0.3,
    }
    buf, _ := json.Marshal(body)
    req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(buf))
    req.Header.Set("Authorization", "Bearer "+c.apiKey)
    req.Header.Set("Content-Type", "application/json")
    resp, err := c.http.Do(req)
    if err != nil {
        return text, err
    }
    defer resp.Body.Close()
    if resp.StatusCode > 299 {
        b, _ := io.ReadAll(resp.Body)
        return text, fmt.Errorf("summarise status %d: %s", resp.StatusCode, string(b))
    }
    var out struct {
        Choices []struct {
            Message struct {
                Content string `json:"content"`
            } `json:"message"`
        } `json:"choices"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
        return text, err
    }
    if len(out.Choices) == 0 {
        return text, errors.New("no summary choices")
    }
    return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

// ExtractPDFText extracts plain text from a PDF. It writes the bytes to a temporary
// file and uses pdftotext, which must be available on the system. Returns the
// extracted text.
func ExtractPDFText(ctx context.Context, pdfBytes []byte) (string, error) {
    tmpDir := os.TempDir()
    // Create temporary PDF file
    pdfFile, err := os.CreateTemp(tmpDir, "in-*.pdf")
    if err != nil {
        return "", err
    }
    pdfName := pdfFile.Name()
    if _, err := pdfFile.Write(pdfBytes); err != nil {
        pdfFile.Close()
        os.Remove(pdfName)
        return "", err
    }
    pdfFile.Close()
    // Run pdftotext: output to stdout by specifying -
    cmd := exec.CommandContext(ctx, "pdftotext", pdfName, "-")
    out, err := cmd.Output()
    os.Remove(pdfName)
    if err != nil {
        return "", err
    }
    return string(out), nil
}