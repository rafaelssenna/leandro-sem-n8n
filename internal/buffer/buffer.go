// internal/buffer/buffer.go
package buffer

import (
	"strings"
	"sync"
	"time"
)

// buffer guarda mensagens, um timer e uma "geração" para invalidar timers antigos.
type buffer struct {
	mu    sync.Mutex
	msgs  []string
	timer *time.Timer
	gen   uint64 // incrementado a cada reset; timers antigos são ignorados
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
// Usa um contador de "geração" para invalidar timers que estejam prestes a disparar.
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

	// invalida timer anterior (se existir) e cria um novo com nova "geração"
	buf.gen++
	currentGen := buf.gen
	if buf.timer != nil {
		buf.timer.Stop() // pode retornar false se já disparou; a geração cuidará disso
	}
	buf.timer = time.AfterFunc(m.timeout, func() { m.flushIfCurrent(phone, currentGen) })
	buf.mu.Unlock()
}

// flushIfCurrent só executa o flush se a geração do timer ainda for a atual.
// Isso evita flush duplo quando uma mensagem chega perto do fim da janela.
func (m *Manager) flushIfCurrent(phone string, genAtSchedule uint64) {
	m.mu.Lock()
	buf, ok := m.buffers[phone]
	m.mu.Unlock()
	if !ok {
		return
	}

	buf.mu.Lock()
	// Se a geração mudou, este timer é obsoleto; não faz nada.
	if genAtSchedule != buf.gen {
		buf.mu.Unlock()
		return
	}
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
