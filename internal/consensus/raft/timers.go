package raft

import "time"

type raftTimer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration) bool
}

type (
	timerFactory        func(d time.Duration) raftTimer
	electionTimeoutFunc func() time.Duration
	tickerFactory       func(d time.Duration) raftTicker
)

type raftTicker interface {
	C() <-chan time.Time
	Stop()
}

type stdTimer struct {
	t *time.Timer
}

func (t *stdTimer) C() <-chan time.Time { return t.t.C }
func (t *stdTimer) Stop() bool          { return t.t.Stop() }
func (t *stdTimer) Reset(d time.Duration) bool {
	return t.t.Reset(d)
}

func defaultTimerFactory(d time.Duration) raftTimer {
	return &stdTimer{t: time.NewTimer(d)}
}

type stdTicker struct {
	t *time.Ticker
}

func (t *stdTicker) C() <-chan time.Time { return t.t.C }
func (t *stdTicker) Stop()               { t.t.Stop() }

func defaultTickerFactory(d time.Duration) raftTicker {
	return &stdTicker{t: time.NewTicker(d)}
}
