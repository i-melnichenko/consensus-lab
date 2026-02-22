package raft

import (
	"fmt"
	"sync"
	"time"
)

type fakeTimerFactory struct {
	mu       sync.Mutex
	timers   []*fakeTimer
	next     int
	createdD []time.Duration
}

func newFakeTimerFactory() *fakeTimerFactory {
	return &fakeTimerFactory{}
}

func (f *fakeTimerFactory) AddTimer() *fakeTimer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := newFakeTimer()
	f.timers = append(f.timers, t)
	return t
}

func (f *fakeTimerFactory) NewTimer(d time.Duration) raftTimer {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdD = append(f.createdD, d)
	if f.next >= len(f.timers) {
		panic(fmt.Sprintf("fakeTimerFactory: no timer prepared for call %d", f.next+1))
	}
	t := f.timers[f.next]
	f.next++
	return t
}

func (f *fakeTimerFactory) CreatedDurations() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Duration, len(f.createdD))
	copy(out, f.createdD)
	return out
}

type fakeTimer struct {
	mu         sync.Mutex
	ch         chan time.Time
	active     bool
	resetCount int
}

func newFakeTimer() *fakeTimer {
	return &fakeTimer{
		ch:     make(chan time.Time, 1),
		active: true,
	}
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := t.active
	t.active = false
	return wasActive
}

func (t *fakeTimer) Reset(_ time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	wasActive := t.active
	t.active = true
	t.resetCount++
	return wasActive
}

func (t *fakeTimer) Fire() {
	t.mu.Lock()
	t.active = false
	t.mu.Unlock()
	select {
	case t.ch <- time.Now():
	default:
	}
}

func (t *fakeTimer) ResetCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resetCount
}

type fakeTickerFactory struct {
	mu       sync.Mutex
	tickers  []*fakeTicker
	next     int
	createdD []time.Duration
}

func newFakeTickerFactory() *fakeTickerFactory {
	return &fakeTickerFactory{}
}

func (f *fakeTickerFactory) AddTicker() *fakeTicker {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := newFakeTicker()
	f.tickers = append(f.tickers, t)
	return t
}

func (f *fakeTickerFactory) NewTicker(d time.Duration) raftTicker {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createdD = append(f.createdD, d)
	if f.next >= len(f.tickers) {
		panic(fmt.Sprintf("fakeTickerFactory: no ticker prepared for call %d", f.next+1))
	}
	t := f.tickers[f.next]
	f.next++
	return t
}

func (f *fakeTickerFactory) CreatedDurations() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]time.Duration, len(f.createdD))
	copy(out, f.createdD)
	return out
}

type fakeTicker struct {
	ch chan time.Time
}

func newFakeTicker() *fakeTicker {
	return &fakeTicker{ch: make(chan time.Time, 1)}
}

func (t *fakeTicker) C() <-chan time.Time { return t.ch }
func (t *fakeTicker) Stop()               {}
func (t *fakeTicker) Fire() {
	select {
	case t.ch <- time.Now():
	default:
	}
}
