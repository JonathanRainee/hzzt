// Package email defines the EmailSender abstraction and its drivers.
package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// Message is a plain-text email.
type Message struct {
	To      string
	Subject string
	Body    string
}

// Sender delivers emails.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Console logs the email instead of sending it (dev driver).
type Console struct{ Log zerolog.Logger }

func (c Console) Send(_ context.Context, m Message) error {
	c.Log.Info().Str("to", m.To).Str("subject", m.Subject).Str("body", m.Body).Msg("email (console driver)")
	return nil
}

// Memory records sent emails (test driver).
type Memory struct {
	mu   sync.Mutex
	sent []Message
}

func (m *Memory) Send(_ context.Context, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

// Sent returns a copy of all recorded messages.
func (m *Memory) Sent() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Message(nil), m.sent...)
}

// Resend sends through the Resend HTTP API.
type Resend struct {
	APIKey string
	From   string
	// BaseURL defaults to https://api.resend.com; overridable in tests.
	BaseURL string
	Client  *http.Client
}

func (r Resend) Send(ctx context.Context, m Message) error {
	base := r.BaseURL
	if base == "" {
		base = "https://api.resend.com"
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	payload, err := json.Marshal(map[string]any{
		"from": r.From, "to": []string{m.To}, "subject": m.Subject, "text": m.Body,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/emails", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.APIKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("resend: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("resend: status %d: %s", res.StatusCode, b)
	}
	return nil
}
