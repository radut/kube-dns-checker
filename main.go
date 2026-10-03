// Command kube-dns-checker probes DNS servers on a schedule and exposes the
// results as Prometheus metrics. See README.md for configuration.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/radut/kube-dns-checker/internal/config"
	"github.com/radut/kube-dns-checker/internal/metrics"
	"github.com/radut/kube-dns-checker/internal/probe"
	"github.com/radut/kube-dns-checker/internal/scheduler"
	"github.com/radut/kube-dns-checker/internal/server"
)

const (
	httpReadTimeout  = 10 * time.Second
	httpWriteTimeout = 10 * time.Second
	shutdownTimeout  = 10 * time.Second
	// A target is considered stuck after this many intervals without a result.
	staleIntervals = 3
	maxStartJitter = time.Second
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	logger := newLogger(cfg)
	slog.SetDefault(logger)

	nameservers, rc, err := probe.ExpandNameservers(cfg.Nameservers, cfg.ResolvConf)
	if err != nil {
		return err
	}
	if rc != nil {
		logger.Info("resolv.conf", "path", cfg.ResolvConf, "nameservers", rc.Nameservers, "search", rc.Search, "ndots", rc.Ndots)
	}
	targets, err := probe.BuildTargets(nameservers, cfg.Domains, cfg.Protocols, cfg.QueryType)
	if err != nil {
		return err
	}
	resolver, err := newResolver(cfg)
	if err != nil {
		return err
	}
	logConfig(logger, cfg, targets)

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m := metrics.New(reg)
	m.Init(targets, string(cfg.Resolver), cfg.QueryType)

	observe := func(res probe.Result, at time.Time) {
		m.Observe(res, at)
		logResult(logger, res)
	}
	sched := scheduler.New(targets, resolver, observe, scheduler.Options{
		Interval:    cfg.Interval,
		Timeout:     cfg.Timeout,
		Attempts:    cfg.Attempts,
		Concurrency: cfg.Concurrency,
		StartJitter: min(cfg.Interval, maxStartJitter),
	})

	maxStaleness := staleIntervals*cfg.Interval + time.Duration(cfg.Attempts)*cfg.Timeout
	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      server.NewHandler(reg, sched, server.Options{MaxStaleness: maxStaleness, Logger: logger}),
		ReadTimeout:  httpReadTimeout,
		WriteTimeout: httpWriteTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()
	go sched.Run(ctx)

	select {
	case err := <-serverErr:
		stop()
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("http shutdown", "err", err)
	}
	return nil
}

func newResolver(cfg config.Config) (probe.Resolver, error) {
	switch cfg.Resolver {
	case config.ResolverGo:
		r := probe.NewGoResolver()
		if !r.SupportsQueryType(cfg.QueryType) {
			return nil, fmt.Errorf("QUERY_TYPE %s is not supported with RESOLVER=go", cfg.QueryType)
		}
		return r, nil
	default:
		return probe.NewDNSClient(), nil
	}
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	opts := &slog.HandlerOptions{Level: level}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stdout, opts))
}

func logConfig(logger *slog.Logger, cfg config.Config, targets []probe.Target) {
	logger.Info("configuration",
		"resolver", cfg.Resolver,
		"domains", cfg.Domains,
		"nameservers", cfg.Nameservers,
		"protocols", cfg.Protocols,
		"query_type", cfg.QueryType,
		"timeout", cfg.Timeout,
		"interval", cfg.Interval,
		"attempts", cfg.Attempts,
		"concurrency", cfg.Concurrency,
		"targets", len(targets),
	)
	for _, t := range targets {
		logger.Debug("target", "nameserver", t.Nameserver, "domain", t.Domain, "protocol", t.Protocol, "type", t.QueryTypeName())
	}
}

func logResult(logger *slog.Logger, res probe.Result) {
	attrs := []any{
		"nameserver", res.Target.Nameserver,
		"domain", res.Target.Domain,
		"protocol", res.Target.Protocol,
		"type", res.Target.QueryTypeName(),
		"duration", res.Duration,
		"attempts", res.Attempts,
	}
	if res.TCPFallback {
		attrs = append(attrs, "tcp_fallback", true)
	}
	if res.Success {
		logger.Info("lookup ok", append(attrs, "answers", res.Answers)...)
		return
	}
	attrs = append(attrs, "reason", res.Reason)
	if res.Rcode != "" {
		attrs = append(attrs, "rcode", res.Rcode)
	}
	if res.Err != nil {
		attrs = append(attrs, "err", res.Err.Error())
	}
	logger.Warn("lookup failed", attrs...)
}
