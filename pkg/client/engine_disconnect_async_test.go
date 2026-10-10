package client

import (
	"context"
	"testing"
	"time"
)

func TestDisconnectAsyncPreservesCompletionBarrier(t *testing.T) {
	e := NewEngine()
	if done := e.DisconnectAsync(); done != nil {
		t.Fatal("idle engine returned teardown barrier")
	}
	g := newConnectionGeneration()
	e.mu.Lock()
	e.generation, e.state = g, StateConnected
	e.mu.Unlock()
	release := make(chan struct{})
	if !g.run(func(context.Context) { <-release }) {
		t.Fatal("worker admission failed")
	}
	var waiting chan struct{}
	defer func() {
		close(release)
		<-g.done
		if waiting != nil {
			<-waiting
		}
	}()
	done := e.DisconnectAsync()
	if done != g.done || g.ctx.Err() == nil {
		t.Fatal("async disconnect did not cancel the captured generation")
	}
	if again := e.DisconnectAsync(); again != done {
		t.Fatal("concurrent request changed teardown barrier")
	}
	select {
	case <-done:
		t.Fatal("teardown completed with a blocked worker")
	default:
	}
	waiting = make(chan struct{})
	go func() { e.Disconnect(); close(waiting) }()
	select {
	case <-waiting:
		t.Fatal("synchronous disconnect lost its wait contract")
	case <-time.After(20 * time.Millisecond):
	}
}
