package buffer

import (
	"strings"
	"sync"
	"time"
)

// Buffer armazena mensagens e um timer por usuário.
type buffer struct {
	mu    sync.Mutex
	msgs  []string
	timer *time.Timer
}

// Manager gerencia buffers por telefone.
type Manager struct {
	mu      sync.Mutex
	buffers map[string]*buffer
	timeout time.Duration
	// callback a ser chamado quando o buffer expira
	flushFunc func(phone, combined string)
}

// NewManager cria um Manager que combina mensagens após 'timeout'
// chamando flushFunc(phone, combined).
func NewManager(timeout time.Duration, flushFunc func(phone, combined string)) *Manager {
	return &Manager{
		buffers:   make(map[string]*buffer),
		timeout:   timeout,
		flushFunc: flushFunc,
	}
}

// AddMessage adiciona uma mensagem ao buffer de um telefone.
// Se ainda não houver timer, inicia um timer para disparar flush após timeout.
func (m *Manager) AddMessage(phone, text string) {
	m.mu.Lock()
	buf, ok := m.buffers[phone]
	if !ok {
		buf = &buffer{}
		m.buffers[phone] = buf
	}
	m.mu.Unlock()

	buf.mu.Lock()
	buf.msgs = append(buf.msgs, text)
	if buf.timer == nil {
		buf.timer = time.AfterFunc(m.timeout, func() {
			m.flush(phone)
		})
	}
	buf.mu.Unlock()
}

// flush é chamado quando o timer expira.
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
