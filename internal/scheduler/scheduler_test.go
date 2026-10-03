package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/radut/kube-dns-checker/internal/config"
	"github.com/radut/kube-dns-checker/internal/dnstest"
	"github.com/radut/kube-dns-checker/internal/probe"
)

type recorder struct {
	mu      sync.Mutex
	results map[string][]probe.Result
}

func newRecorder() *recorder { return &recorder{results: map[string][]probe.Result{}} }

func (r *recorder) observe(res probe.Result, _ time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results[res.Target.Domain] = append(r.results[res.Target.Domain], res)
}

func (r *recorder) get(domain string) []probe.Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]probe.Result(nil), r.results[domain]...)
}

func (r *recorder) count(domain string) int { return len(r.get(domain)) }

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func targets(server string, domains ...string) []probe.Target {
	out := make([]probe.Target, 0, len(domains))
	for _, d := range domains {
		out = append(out, probe.Target{Nameserver: server, Domain: d, Protocol: config.ProtocolUDP, QueryType: dns.TypeA})
	}
	return out
}

func TestSchedulerProbesEachTargetIndependently(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("fast.test", dnstest.Behavior{Answers: []string{"10.0.0.1"}})
	srv.Set("dead.test", dnstest.Behavior{Drop: true}) // always times out

	rec := newRecorder()
	s := New(targets(srv.Addr, "fast.test", "dead.test"), probe.NewDNSClient(), rec.observe, Options{
		Interval: 40 * time.Millisecond, Timeout: 150 * time.Millisecond, Attempts: 1, Concurrency: 4,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	// The dead target needs 150ms per probe; the fast one must keep sampling at ~40ms.
	waitFor(t, 2*time.Second, func() bool { return rec.count("fast.test") >= 5 })
	waitFor(t, 2*time.Second, func() bool { return rec.count("dead.test") >= 2 })
	cancel()
	<-done

	if s.Stale(0) == nil {
		t.Error("Stale with zero age should report every target")
	}
	for _, r := range rec.get("fast.test") {
		if !r.Success {
			t.Errorf("fast probe failed: %+v", r)
		}
	}
	for _, r := range rec.get("dead.test") {
		if r.Success || r.Reason != probe.ReasonTimeout {
			t.Errorf("dead probe should time out: %+v", r)
		}
	}
	if rec.count("fast.test") <= rec.count("dead.test") {
		t.Errorf("fast target (%d) should be sampled more often than the dead one (%d)", rec.count("fast.test"), rec.count("dead.test"))
	}
}

func TestSchedulerFlakyServerRecordsBothOutcomes(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("flaky.test", dnstest.Behavior{Answers: []string{"10.0.0.1"}, FlakyEvery: 2, FlakyMode: dnstest.FlakyServfail})

	rec := newRecorder()
	s := New(targets(srv.Addr, "flaky.test"), probe.NewDNSClient(), rec.observe, Options{
		Interval: 20 * time.Millisecond, Timeout: 100 * time.Millisecond, Attempts: 1, Concurrency: 1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, 2*time.Second, func() bool { return rec.count("flaky.test") >= 6 })
	cancel()
	<-done

	var ok, failed int
	for _, r := range rec.get("flaky.test") {
		if r.Success {
			ok++
		} else if r.Reason == "SERVFAIL" {
			failed++
		} else {
			t.Errorf("unexpected failure reason: %+v", r)
		}
	}
	if ok == 0 || failed == 0 {
		t.Errorf("expected both successes and SERVFAILs, got ok=%d failed=%d", ok, failed)
	}
}

func TestSchedulerRetriesHideSingleFlakes(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("flaky.test", dnstest.Behavior{Answers: []string{"10.0.0.1"}, FlakyEvery: 2, FlakyMode: dnstest.FlakyDrop})

	rec := newRecorder()
	s := New(targets(srv.Addr, "flaky.test"), probe.NewDNSClient(), rec.observe, Options{
		Interval: 20 * time.Millisecond, Timeout: 60 * time.Millisecond, Attempts: 2, Concurrency: 1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, 3*time.Second, func() bool { return rec.count("flaky.test") >= 4 })
	cancel()
	<-done

	for _, r := range rec.get("flaky.test") {
		if !r.Success {
			t.Errorf("with 2 attempts every probe should succeed: %+v", r)
		}
	}
}

// slowResolver blocks for a fixed time and counts concurrent calls.
type slowResolver struct {
	delay   time.Duration
	inUse   atomic.Int32
	maxSeen atomic.Int32
}

func (s *slowResolver) Query(ctx context.Context, tgt probe.Target) probe.Result {
	n := s.inUse.Add(1)
	defer s.inUse.Add(-1)
	for {
		seen := s.maxSeen.Load()
		if n <= seen || s.maxSeen.CompareAndSwap(seen, n) {
			break
		}
	}
	select {
	case <-time.After(s.delay):
	case <-ctx.Done():
	}
	return probe.Result{Target: tgt, Success: true, Duration: s.delay}
}

func TestSchedulerNeverOverlapsSameTargetAndRespectsConcurrency(t *testing.T) {
	r := &slowResolver{delay: 50 * time.Millisecond}
	rec := newRecorder()
	s := New(targets("x:53", "a.", "b.", "c.", "d."), r, rec.observe, Options{
		Interval: 10 * time.Millisecond, Timeout: time.Second, Attempts: 1, Concurrency: 2,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, 3*time.Second, func() bool { return rec.count("a.") >= 3 && rec.count("d.") >= 3 })
	cancel()
	<-done

	if got := r.maxSeen.Load(); got > 2 {
		t.Errorf("max concurrent probes = %d, want <= 2", got)
	}
}

func TestSchedulerStaleAndStarted(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	srv := dnstest.Start(t)
	srv.Set("ok.test", dnstest.Behavior{Answers: []string{"10.0.0.1"}})

	s := New(targets(srv.Addr, "ok.test"), probe.NewDNSClient(), func(probe.Result, time.Time) {}, Options{
		Interval: time.Hour, Timeout: 200 * time.Millisecond, Attempts: 1, Concurrency: 1, Now: clock,
	})
	if s.Started() {
		t.Error("should not be started before Run")
	}
	if len(s.Stale(time.Minute)) != 1 {
		t.Error("target without any result should be stale")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, 2*time.Second, func() bool { return s.Started() && len(s.Stale(time.Minute)) == 0 })

	now = now.Add(2 * time.Minute)
	if len(s.Stale(time.Minute)) != 1 {
		t.Error("target should be stale after the clock advances past maxAge")
	}
	cancel()
	<-done
}

func TestSchedulerGracePeriodForFirstProbe(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	s := New(targets("x:53", "never."), &slowResolver{delay: time.Hour}, func(probe.Result, time.Time) {}, Options{
		Interval: time.Hour, Timeout: time.Hour, Attempts: 1, Concurrency: 1, Now: clock,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, 2*time.Second, s.Started)

	if stale := s.Stale(time.Minute); len(stale) != 0 {
		t.Errorf("fresh scheduler must not report stale targets, got %v", stale)
	}
	now = now.Add(2 * time.Minute)
	if stale := s.Stale(time.Minute); len(stale) != 1 {
		t.Errorf("target that never completed should be stale after the grace period, got %v", stale)
	}
	cancel()
	<-done
}

func TestSchedulerDropsResultsDuringShutdown(t *testing.T) {
	srv := dnstest.Start(t)
	srv.Set("slow.test", dnstest.Behavior{Answers: []string{"10.0.0.1"}, Delay: 300 * time.Millisecond})

	var observed atomic.Int32
	s := New(targets(srv.Addr, "slow.test"), probe.NewDNSClient(), func(probe.Result, time.Time) { observed.Add(1) }, Options{
		Interval: time.Hour, Timeout: time.Second, Attempts: 1, Concurrency: 1,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	time.Sleep(50 * time.Millisecond) // probe is in flight
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if observed.Load() != 0 {
		t.Errorf("a probe cancelled by shutdown must not be reported as a result")
	}
}
