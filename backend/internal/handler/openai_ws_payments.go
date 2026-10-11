package handler

import (
	"context"
	"sync"
)

// A points reservation pays one response, never an entire WebSocket session.
// Hooks can run on separate readers, so keep immutable contexts per turn.
type openAIWSTurnPayment struct {
	ctx  context.Context
	done func()
}

type openAIWSTurnPayments struct {
	mu    sync.Mutex
	turns map[int]openAIWSTurnPayment
}

func (p *openAIWSTurnPayments) begin(turn int, reserve func() (context.Context, func(), error)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.turns[turn]; ok {
		return nil
	}
	ctx, done, err := reserve()
	if err != nil {
		return err
	}
	if p.turns == nil {
		p.turns = make(map[int]openAIWSTurnPayment)
	}
	p.turns[turn] = openAIWSTurnPayment{ctx: ctx, done: done}
	return nil
}

func (p *openAIWSTurnPayments) context(turn int, fallback context.Context) context.Context {
	p.mu.Lock()
	defer p.mu.Unlock()
	if payment, ok := p.turns[turn]; ok {
		return payment.ctx
	}
	return fallback
}

func (p *openAIWSTurnPayments) release(turn int) {
	p.mu.Lock()
	payment, ok := p.turns[turn]
	delete(p.turns, turn)
	p.mu.Unlock()
	if ok && payment.done != nil {
		payment.done()
	}
}

func (p *openAIWSTurnPayments) close() {
	p.mu.Lock()
	turns := p.turns
	p.turns = nil
	p.mu.Unlock()
	for _, payment := range turns {
		if payment.done != nil {
			payment.done()
		}
	}
}
