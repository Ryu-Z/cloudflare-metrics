package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"time"
	_ "time/tzdata"
)

func main() {
	configPath := flag.String("config", "config.yaml", "Path to config file")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	location, err := loadLocation(cfg.Schedule.Timezone)
	if err != nil {
		log.Fatalf("load timezone: %v", err)
	}

	exporter := NewExporter(cfg, location)
	notifier := NewLarkNotifier(cfg.Alerting.Lark)
	runCollectionJob(exporter, cfg, notifier)

	go runScheduler(exporter, cfg, notifier, location)

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(exporter.RenderMetrics()))
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if exporter.LastError() != "" {
			http.Error(w, exporter.LastError(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	log.Printf("cloudflare analytics exporter listening on %s", cfg.Metrics.ListenAddr)
	if err := http.ListenAndServe(cfg.Metrics.ListenAddr, mux); err != nil {
		log.Fatalf("listen: %v", err)
	}
}

func loadLocation(name string) (*time.Location, error) {
	if name == "" || name == "Local" {
		return time.Local, nil
	}
	return time.LoadLocation(name)
}

func runScheduler(exporter *Exporter, cfg Config, notifier *LarkNotifier, loc *time.Location) {
	if cfg.Schedule.IntervalMinutes > 0 {
		runIntervalScheduler(exporter, cfg, notifier, cfg.Schedule.IntervalMinutes)
		return
	}
	runDailyScheduler(exporter, cfg, notifier, loc, cfg.Schedule.Daily)
}

func runIntervalScheduler(exporter *Exporter, cfg Config, notifier *LarkNotifier, intervalMinutes int) {
	interval := time.Duration(intervalMinutes) * time.Minute
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for range ticker.C {
		runCollectionJob(exporter, cfg, notifier)
	}
}

func runDailyScheduler(exporter *Exporter, cfg Config, notifier *LarkNotifier, loc *time.Location, daily DailySchedule) {
	for {
		now := time.Now().In(loc)
		next := time.Date(now.Year(), now.Month(), now.Day(), daily.Hour, daily.Minute, 0, 0, loc)
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		time.Sleep(time.Until(next))
		runCollectionJob(exporter, cfg, notifier)
	}
}

func runCollectionJob(exporter *Exporter, cfg Config, notifier *LarkNotifier) {
	attempts := cfg.Schedule.Retry.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	delay := time.Duration(cfg.Schedule.Retry.DelaySeconds) * time.Second
	if delay < 0 {
		delay = 0
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		lastErr = exporter.CollectAll(ctx)
		cancel()
		if lastErr == nil {
			return
		}

		log.Printf("collection attempt %d/%d failed: %v", attempt, attempts, lastErr)
		if attempt < attempts && delay > 0 {
			time.Sleep(delay)
		}
	}

	if notifier != nil {
		if err := notifier.NotifyCollectionFailure(cfg, exporter, lastErr, attempts); err != nil {
			log.Printf("send lark alert failed: %v", err)
		} else {
			log.Printf("lark alert sent successfully after %d failed attempts", attempts)
		}
	}
}
