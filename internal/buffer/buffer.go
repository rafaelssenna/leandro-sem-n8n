// internal/buffer/buffer.go
package buffer

import (
	"strings"
	"sync"
	"time"
)

// buffer guarda mensagens e um timer por telefone.
type buffer struct {
	mu    sync.Mutex
	msgs  []string
	timer *time.Timer
}

// Manager gerencia buffers por telefone e dispara o flush após timeout
// chamando flushFunc(phone, combinedText).
type Manager struct {
	mu        sync.Mutex
	buffers   map[string]*buffer
	timeout   time.Duration
	flushFunc func(phone, combined string)
}

func NewManager(timeout time.Duration, flushFunc func(phone, combined string)) *Manager {
	return &Manager{
		buffers:   make(map[string]*buffer),
		timeout:   timeout,
		flushFunc: flushFunc,
	}
}

// AddMessage adiciona a mensagem ao buffer do telefone e reinicia o timer
// (debounce deslizante). Mensagens consecutivas iguais são ignoradas.
func (m *Manager) AddMessage(phone, text string) {
	normalized := strings.TrimSpace(text)
	if normalized == "" {
		return
	}

	m.mu.Lock()
	buf, ok := m.buffers[phone]
	if !ok {
		buf = &buffer{}
		m.buffers[phone] = buf
	}
	m.mu.Unlock()

	buf.mu.Lock()
	// dedupe consecutivo
	n := len(buf.msgs)
	if n == 0 || buf.msgs[n-1] != normalized {
		buf.msgs = append(buf.msgs, normalized)
	}
	if buf.timer == nil {
		buf.timer = time.AfterFunc(m.timeout, func() { m.flush(phone) })
	} else {
		// debounce deslizante: sempre reinicia o timer a cada nova mensagem
		buf.timer.Reset(m.timeout)
	}
	buf.mu.Unlock()
}

// flush é chamado quando a janela de inatividade expira.
func (m *Manager) flush(phone string) {
	m.mu.Lock()
	buf, ok := m.buffers[phone]
	if !ok {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	buf.mu.Lock()
	msgs := buf.msgs
	buf.msgs = nil
	buf.timer = nil
	buf.mu.Unlock()

	if len(msgs) > 0 {
		combined := "Mensagens recentes do usuário:\n- " + strings.Join(msgs, "\n- ")
		m.flushFunc(phone, combined)
	}

	m.mu.Lock()
	delete(m.buffers, phone)
	m.mu.Unlock()
}
