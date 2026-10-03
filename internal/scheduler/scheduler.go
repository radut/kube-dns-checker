// Package scheduler runs each probe target on its own ticker so that a slow
// or dead nameserver never delays the sampling of the others.
package scheduler

import (
	"context"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/radut/kube-dns-checker/internal/probe"
)

// Options configure a Scheduler.
type Options struct {
	Interval    time.Duration // time between probes of the same target
	Timeout     time.Duration // per attempt
	Attempts    int
	Concurrency int           // max probes in flight across all targets
	StartJitter time.Duration // first probe is delayed by a random amount up to this
	Now         func() time.Time
}

// Scheduler owns the probe goroutines.
type Scheduler struct {
	targets  []probe.Target
	resolver probe.Resolver
	observe  func(probe.Result, time.Time)
	opts     Options
	sem      chan struct{}
	started  atomic.Bool
	startMu  sync.Mutex
	startAt  time.Time
	lastRun  sync.Map // target key -> time.Time
}

// New creates a scheduler. observe is called with every result.
func New(targets []probe.Target, resolver probe.Resolver, observe func(probe.Result, time.Time), opts Options) *Scheduler {
	if opts.Concurrency < 1 {
		opts.Concurrency = 1
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Scheduler{
		targets:  targets,
		resolver: resolver,
		observe:  observe,
		opts:     opts,
		sem:      make(chan struct{}, opts.Concurrency),
	}
}

// Run starts one loop per target and blocks until ctx is cancelled and all
// in-flight probes have returned.
func (s *Scheduler) Run(ctx context.Context) {
	s.startMu.Lock()
	s.startAt = s.opts.Now()
	s.startMu.Unlock()

	var wg sync.WaitGroup
	for _, t := range s.targets {
		wg.Add(1)
		go func(t probe.Target) {
			defer wg.Done()
			s.loop(ctx, t)
		}(t)
	}
	s.started.Store(true)
	wg.Wait()
}

func (s *Scheduler) loop(ctx context.Context, t probe.Target) {
	if !sleepCtx(ctx, s.jitter()) {
		return
	}
	s.probeOnce(ctx, t)

	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// A probe slower than the interval simply drops ticks; the same
			// target never runs concurrently with itself.
			s.probeOnce(ctx, t)
		}
	}
}

func (s *Scheduler) probeOnce(ctx context.Context, t probe.Target) {
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-s.sem }()

	res := probe.Run(ctx, s.resolver, t, s.opts.Attempts, s.opts.Timeout)
	if ctx.Err() != nil {
		return // shutting down; a cancelled probe is not a DNS failure
	}
	now := s.opts.Now()
	s.lastRun.Store(t.Key(), now)
	s.observe(res, now)
}

func (s *Scheduler) jitter() time.Duration {
	if s.opts.StartJitter <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(s.opts.StartJitter)))
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Started reports whether the probe loops have been launched.
func (s *Scheduler) Started() bool {
	return s.started.Load()
}

// Stale returns the targets whose most recent probe is older than maxAge.
// A target that has never produced a result counts as stale once maxAge has
// passed since the scheduler started, which gives the first probe (start
// jitter plus attempts) time to complete without failing liveness.
func (s *Scheduler) Stale(maxAge time.Duration) []probe.Target {
	now := s.opts.Now()
	s.startMu.Lock()
	startAt := s.startAt
	s.startMu.Unlock()

	var stale []probe.Target
	for _, t := range s.targets {
		last := startAt
		if v, ok := s.lastRun.Load(t.Key()); ok {
			last = v.(time.Time)
		}
		if now.Sub(last) > maxAge {
			stale = append(stale, t)
		}
	}
	return stale
}
