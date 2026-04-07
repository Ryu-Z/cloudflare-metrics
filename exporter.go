package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Sample struct {
	Metric string
	Labels map[string]string
	Value  float64
}

type Exporter struct {
	cfg                 Config
	client              *CloudflareClient
	location            *time.Location
	mu                  sync.RWMutex
	samples             []Sample
	counters            map[string]float64
	lastError           string
	lastPartial         string
	lastNotifiedPartial string
	lastSuccess         time.Time
}

type CollectionFailure struct {
	ZoneDomain string
	Stage      string
	Err        error
}

func (f *CollectionFailure) Error() string {
	if f == nil {
		return ""
	}
	if f.ZoneDomain == "" {
		return fmt.Sprintf("%s: %v", f.Stage, f.Err)
	}
	return fmt.Sprintf("zone %s %s: %v", f.ZoneDomain, f.Stage, f.Err)
}

func newCollectionFailure(zoneDomain, stage string, err error) error {
	if err == nil {
		return nil
	}
	return &CollectionFailure{
		ZoneDomain: zoneDomain,
		Stage:      stage,
		Err:        err,
	}
}

func NewExporter(cfg Config, location *time.Location) *Exporter {
	return &Exporter{
		cfg:      cfg,
		client:   NewCloudflareClient(cfg.Cloudflare.APIToken),
		location: location,
		counters: map[string]float64{},
	}
}

func (e *Exporter) CollectAll(ctx context.Context) error {
	if err := e.collect(ctx); err != nil {
		log.Printf("collection failed: %v", err)
		e.mu.Lock()
		e.lastError = err.Error()
		e.mu.Unlock()
		return err
	}
	return nil
}

func (e *Exporter) collect(ctx context.Context) error {
	now := time.Now().UTC()
	todayStart := startOfDay(now)
	dailyDate := now.AddDate(0, 0, -1)
	currentMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	lastMonthStart := currentMonthStart.AddDate(0, -1, 0)
	lastMonthEnd := currentMonthStart.Add(-time.Second)
	lastMonthToDateEnd := previousMonthEquivalent(now, time.UTC)
	closedMonthStart, closedMonthEnd, closedMonthDateStart, closedMonthDateEnd := latestClosedMonthRange(time.Now().In(e.location))
	previousSamples := e.snapshotSamples()
	var partialFailures []string

	nextSamples := make([]Sample, 0, 256)
	accountTodayAll := UsageNumbers{}
	accountDailyAll := UsageNumbers{}
	accountMonthlyAll := UsageNumbers{}
	accountLastMonthAll := UsageNumbers{}
	accountClosedMonthAll := UsageNumbers{}
	accountLastMonthToDateAll := UsageNumbers{}
	accountTodayHTTP := UsageNumbers{}
	accountDailyHTTP := UsageNumbers{}
	accountMonthlyHTTP := UsageNumbers{}
	accountLastMonthHTTP := UsageNumbers{}
	accountClosedMonthHTTP := UsageNumbers{}
	accountLastMonthToDateHTTP := UsageNumbers{}
	accountTodaySpectrum := UsageNumbers{}
	accountDailySpectrum := UsageNumbers{}
	accountMonthlySpectrum := UsageNumbers{}
	accountLastMonthSpectrum := UsageNumbers{}
	accountClosedMonthSpectrum := UsageNumbers{}
	accountLastMonthToDateSpectrum := UsageNumbers{}

	for _, zone := range e.cfg.Cloudflare.Zones {
		baseLabels := map[string]string{
			"scope":       "zone",
			"account_id":  e.cfg.Cloudflare.AccountID,
			"zone_id":     zone.ZoneID,
			"zone_name":   zone.Name,
			"zone_domain": zone.Domain,
		}

		httpDaily, err := e.client.FetchHTTPUsage(ctx, zone.ZoneID, dailyDate.Format("2006-01-02"), dailyDate.Format("2006-01-02"))
		e.bumpQueryCounter(zone.ZoneID, zone.Domain, statusLabel(err))
		if err != nil {
			return newCollectionFailure(zone.Domain, "daily_http", err)
		}
		httpToday, err := e.client.FetchHTTPUsage(ctx, zone.ZoneID, now.Format("2006-01-02"), now.Format("2006-01-02"))
		e.bumpQueryCounter(zone.ZoneID, zone.Domain, statusLabel(err))
		if err != nil {
			return newCollectionFailure(zone.Domain, "today_http", err)
		}
		httpMonthly, err := e.client.FetchHTTPUsage(ctx, zone.ZoneID, currentMonthStart.Format("2006-01-02"), now.Format("2006-01-02"))
		e.bumpQueryCounter(zone.ZoneID, zone.Domain, statusLabel(err))
		if err != nil {
			return newCollectionFailure(zone.Domain, "monthly_http", err)
		}
		httpLastMonth, err := e.client.FetchHTTPUsage(ctx, zone.ZoneID, lastMonthStart.Format("2006-01-02"), lastMonthEnd.Format("2006-01-02"))
		e.bumpQueryCounter(zone.ZoneID, zone.Domain, statusLabel(err))
		if err != nil {
			return newCollectionFailure(zone.Domain, "last_month_http", err)
		}
		httpClosedMonth, err := e.client.FetchHTTPUsage(ctx, zone.ZoneID, closedMonthDateStart, closedMonthDateEnd)
		e.bumpQueryCounter(zone.ZoneID, zone.Domain, statusLabel(err))
		if err != nil {
			return newCollectionFailure(zone.Domain, "closed_month_http", err)
		}
		httpLastMonthToDate, err := e.client.FetchHTTPUsage(ctx, zone.ZoneID, lastMonthStart.Format("2006-01-02"), lastMonthToDateEnd.Format("2006-01-02"))
		e.bumpQueryCounter(zone.ZoneID, zone.Domain, statusLabel(err))
		if err != nil {
			return newCollectionFailure(zone.Domain, "last_month_to_date_http", err)
		}

		spectrumDaily, err := e.client.FetchSpectrumUsage(ctx, zone.SpectrumZoneID, startOfDay(dailyDate), endOfDay(dailyDate))
		e.bumpQueryCounter(zone.ZoneID, zone.Domain, statusLabel(err))
		if err != nil {
			spectrumDaily = e.fallbackUsage(previousSamples, "daily", "spectrum", baseLabels)
			log.Printf("zone %s daily spectrum query failed, reusing previous values: %v", zone.Domain, err)
			partialFailures = append(partialFailures, fmt.Sprintf("%s:daily_spectrum", zone.Domain))
		}
		spectrumToday, err := e.client.FetchSpectrumUsage(ctx, zone.SpectrumZoneID, todayStart, now)
		e.bumpQueryCounter(zone.ZoneID, zone.Domain, statusLabel(err))
		if err != nil {
			spectrumToday = e.fallbackUsage(previousSamples, "today", "spectrum", baseLabels)
			log.Printf("zone %s today spectrum query failed, reusing previous values: %v", zone.Domain, err)
			partialFailures = append(partialFailures, fmt.Sprintf("%s:today_spectrum", zone.Domain))
		}
		spectrumMonthly, err := e.client.FetchSpectrumUsage(ctx, zone.SpectrumZoneID, currentMonthStart, now)
		e.bumpQueryCounter(zone.ZoneID, zone.Domain, statusLabel(err))
		if err != nil {
			spectrumMonthly = e.fallbackUsage(previousSamples, "monthly", "spectrum", baseLabels)
			log.Printf("zone %s monthly spectrum query failed, reusing previous values: %v", zone.Domain, err)
			partialFailures = append(partialFailures, fmt.Sprintf("%s:monthly_spectrum", zone.Domain))
		}
		spectrumLastMonth, err := e.client.FetchSpectrumUsage(ctx, zone.SpectrumZoneID, lastMonthStart, lastMonthEnd)
		e.bumpQueryCounter(zone.ZoneID, zone.Domain, statusLabel(err))
		if err != nil {
			spectrumLastMonth = e.fallbackUsage(previousSamples, "last_month", "spectrum", baseLabels)
			log.Printf("zone %s last month spectrum query failed, reusing previous values: %v", zone.Domain, err)
			partialFailures = append(partialFailures, fmt.Sprintf("%s:last_month_spectrum", zone.Domain))
		}
		spectrumClosedMonth, err := e.client.FetchSpectrumUsage(ctx, zone.SpectrumZoneID, closedMonthStart, closedMonthEnd)
		e.bumpQueryCounter(zone.ZoneID, zone.Domain, statusLabel(err))
		if err != nil {
			spectrumClosedMonth = e.fallbackUsage(previousSamples, "closed_month", "spectrum", baseLabels)
			log.Printf("zone %s closed month spectrum query failed, reusing previous values: %v", zone.Domain, err)
			partialFailures = append(partialFailures, fmt.Sprintf("%s:closed_month_spectrum", zone.Domain))
		}
		spectrumLastMonthToDate, err := e.client.FetchSpectrumUsage(ctx, zone.SpectrumZoneID, lastMonthStart, lastMonthToDateEnd)
		e.bumpQueryCounter(zone.ZoneID, zone.Domain, statusLabel(err))
		if err != nil {
			spectrumLastMonthToDate = e.fallbackUsage(previousSamples, "last_month_to_date", "spectrum", baseLabels)
			log.Printf("zone %s last month-to-date spectrum query failed, reusing previous values: %v", zone.Domain, err)
			partialFailures = append(partialFailures, fmt.Sprintf("%s:last_month_to_date_spectrum", zone.Domain))
		}

		todayAll := mergeUsage(httpToday, spectrumToday)
		dailyAll := mergeUsage(httpDaily, spectrumDaily)
		monthlyAll := mergeUsage(httpMonthly, spectrumMonthly)
		lastMonthAll := mergeUsage(httpLastMonth, spectrumLastMonth)
		closedMonthAll := mergeUsage(httpClosedMonth, spectrumClosedMonth)
		lastMonthToDateAll := mergeUsage(httpLastMonthToDate, spectrumLastMonthToDate)

		nextSamples = append(nextSamples, usageSamples("today", "all", todayAll, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("today", "http", httpToday, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("today", "spectrum", spectrumToday, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("daily", "all", dailyAll, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("daily", "http", httpDaily, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("daily", "spectrum", spectrumDaily, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("monthly", "all", monthlyAll, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("monthly", "http", httpMonthly, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("monthly", "spectrum", spectrumMonthly, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("last_month", "all", lastMonthAll, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("last_month", "http", httpLastMonth, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("last_month", "spectrum", spectrumLastMonth, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("closed_month", "all", closedMonthAll, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("closed_month", "http", httpClosedMonth, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("closed_month", "spectrum", spectrumClosedMonth, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("last_month_to_date", "all", lastMonthToDateAll, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("last_month_to_date", "http", httpLastMonthToDate, baseLabels)...)
		nextSamples = append(nextSamples, usageSamples("last_month_to_date", "spectrum", spectrumLastMonthToDate, baseLabels)...)
		nextSamples = append(nextSamples, Sample{
			Metric: "cloudflare_analytics_last_success_timestamp",
			Labels: cloneLabels(baseLabels),
			Value:  float64(time.Now().Unix()),
		})

		accountTodayAll = mergeUsage(accountTodayAll, todayAll)
		accountDailyAll = mergeUsage(accountDailyAll, dailyAll)
		accountMonthlyAll = mergeUsage(accountMonthlyAll, monthlyAll)
		accountLastMonthAll = mergeUsage(accountLastMonthAll, lastMonthAll)
		accountClosedMonthAll = mergeUsage(accountClosedMonthAll, closedMonthAll)
		accountLastMonthToDateAll = mergeUsage(accountLastMonthToDateAll, lastMonthToDateAll)
		accountTodayHTTP = mergeUsage(accountTodayHTTP, httpToday)
		accountDailyHTTP = mergeUsage(accountDailyHTTP, httpDaily)
		accountMonthlyHTTP = mergeUsage(accountMonthlyHTTP, httpMonthly)
		accountLastMonthHTTP = mergeUsage(accountLastMonthHTTP, httpLastMonth)
		accountClosedMonthHTTP = mergeUsage(accountClosedMonthHTTP, httpClosedMonth)
		accountLastMonthToDateHTTP = mergeUsage(accountLastMonthToDateHTTP, httpLastMonthToDate)
		accountTodaySpectrum = mergeUsage(accountTodaySpectrum, spectrumToday)
		accountDailySpectrum = mergeUsage(accountDailySpectrum, spectrumDaily)
		accountMonthlySpectrum = mergeUsage(accountMonthlySpectrum, spectrumMonthly)
		accountLastMonthSpectrum = mergeUsage(accountLastMonthSpectrum, spectrumLastMonth)
		accountClosedMonthSpectrum = mergeUsage(accountClosedMonthSpectrum, spectrumClosedMonth)
		accountLastMonthToDateSpectrum = mergeUsage(accountLastMonthToDateSpectrum, spectrumLastMonthToDate)
	}

	if e.cfg.Cloudflare.AccountID != "" {
		accountLabels := map[string]string{
			"scope":       "account",
			"account_id":  e.cfg.Cloudflare.AccountID,
			"zone_id":     "",
			"zone_name":   "",
			"zone_domain": "",
		}

		nextSamples = append(nextSamples, usageSamples("today", "all", accountTodayAll, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("today", "http", accountTodayHTTP, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("today", "spectrum", accountTodaySpectrum, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("daily", "all", accountDailyAll, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("daily", "http", accountDailyHTTP, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("daily", "spectrum", accountDailySpectrum, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("monthly", "all", accountMonthlyAll, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("monthly", "http", accountMonthlyHTTP, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("monthly", "spectrum", accountMonthlySpectrum, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("last_month", "all", accountLastMonthAll, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("last_month", "http", accountLastMonthHTTP, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("last_month", "spectrum", accountLastMonthSpectrum, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("closed_month", "all", accountClosedMonthAll, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("closed_month", "http", accountClosedMonthHTTP, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("closed_month", "spectrum", accountClosedMonthSpectrum, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("last_month_to_date", "all", accountLastMonthToDateAll, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("last_month_to_date", "http", accountLastMonthToDateHTTP, accountLabels)...)
		nextSamples = append(nextSamples, usageSamples("last_month_to_date", "spectrum", accountLastMonthToDateSpectrum, accountLabels)...)
		nextSamples = append(nextSamples, Sample{
			Metric: "cloudflare_analytics_last_success_timestamp",
			Labels: cloneLabels(accountLabels),
			Value:  float64(time.Now().Unix()),
		})
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.samples = nextSamples
	e.lastError = ""
	e.lastPartial = strings.Join(partialFailures, ",")
	e.lastSuccess = time.Now()
	return nil
}

func (e *Exporter) RenderMetrics() string {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var b strings.Builder
	writeMetricHeader(&b, "cloudflare_bytes_total_today", "Gauge", "Current day's cumulative total bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_ingress_today", "Gauge", "Current day's cumulative ingress bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_egress_today", "Gauge", "Current day's cumulative egress bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_cached_today", "Gauge", "Current day's cumulative cached HTTP bytes.")
	writeMetricHeader(&b, "cloudflare_requests_total_today", "Gauge", "Current day's cumulative requests or connection count across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_total_daily", "Gauge", "Previous full day's total bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_ingress_daily", "Gauge", "Previous full day's ingress bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_egress_daily", "Gauge", "Previous full day's egress bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_cached_daily", "Gauge", "Previous full day's cached HTTP bytes.")
	writeMetricHeader(&b, "cloudflare_requests_total_daily", "Gauge", "Previous full day's requests or connection count across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_total_monthly", "Gauge", "Current month cumulative total bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_ingress_monthly", "Gauge", "Current month cumulative ingress bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_egress_monthly", "Gauge", "Current month cumulative egress bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_cached_monthly", "Gauge", "Current month cumulative cached HTTP bytes.")
	writeMetricHeader(&b, "cloudflare_requests_total_monthly", "Gauge", "Current month cumulative requests or connection count across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_total_last_month", "Gauge", "Previous calendar month's total bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_ingress_last_month", "Gauge", "Previous calendar month's ingress bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_egress_last_month", "Gauge", "Previous calendar month's egress bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_cached_last_month", "Gauge", "Previous calendar month's cached HTTP bytes.")
	writeMetricHeader(&b, "cloudflare_requests_total_last_month", "Gauge", "Previous calendar month's requests or connection count across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_total_closed_month", "Gauge", "Latest fully closed calendar month's total bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_ingress_closed_month", "Gauge", "Latest fully closed calendar month's ingress bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_egress_closed_month", "Gauge", "Latest fully closed calendar month's egress bytes across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_cached_closed_month", "Gauge", "Latest fully closed calendar month's cached HTTP bytes.")
	writeMetricHeader(&b, "cloudflare_requests_total_closed_month", "Gauge", "Latest fully closed calendar month's requests or connection count across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_total_last_month_to_date", "Gauge", "Previous month's cumulative bytes through the equivalent day-of-month across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_ingress_last_month_to_date", "Gauge", "Previous month's cumulative ingress bytes through the equivalent day-of-month across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_egress_last_month_to_date", "Gauge", "Previous month's cumulative egress bytes through the equivalent day-of-month across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_bytes_cached_last_month_to_date", "Gauge", "Previous month's cumulative cached HTTP bytes through the equivalent day-of-month.")
	writeMetricHeader(&b, "cloudflare_requests_total_last_month_to_date", "Gauge", "Previous month's cumulative requests or connection count through the equivalent day-of-month across the selected product scope.")
	writeMetricHeader(&b, "cloudflare_analytics_query_total", "Counter", "Total number of upstream Cloudflare analytics API calls.")
	writeMetricHeader(&b, "cloudflare_analytics_last_success_timestamp", "Gauge", "Unix timestamp of the last successful collection.")
	writeMetricHeader(&b, "cloudflare_analytics_partial_failure", "Gauge", "Whether the latest collection reused previous values for any partial upstream failure.")
	writeMetricHeader(&b, "cloudflare_analytics_up", "Gauge", "Whether the exporter has a fresh successful snapshot.")

	for _, sample := range e.samples {
		b.WriteString(sample.Metric)
		b.WriteString(formatLabels(sample.Labels))
		b.WriteByte(' ')
		b.WriteString(strconv.FormatFloat(sample.Value, 'f', -1, 64))
		b.WriteByte('\n')
	}

	keys := make([]string, 0, len(e.counters))
	for key := range e.counters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts := strings.Split(key, "\x00")
		labels := map[string]string{
			"zone_id":     parts[0],
			"zone_domain": parts[1],
			"status":      parts[2],
		}
		b.WriteString("cloudflare_analytics_query_total")
		b.WriteString(formatLabels(labels))
		b.WriteByte(' ')
		b.WriteString(strconv.FormatFloat(e.counters[key], 'f', -1, 64))
		b.WriteByte('\n')
	}

	partial := 0.0
	if e.lastPartial != "" {
		partial = 1
	}
	b.WriteString("cloudflare_analytics_partial_failure ")
	b.WriteString(strconv.FormatFloat(partial, 'f', -1, 64))
	b.WriteByte('\n')

	up := 1.0
	if len(e.samples) == 0 || e.lastError != "" {
		up = 0
	}
	b.WriteString("cloudflare_analytics_up ")
	b.WriteString(strconv.FormatFloat(up, 'f', -1, 64))
	b.WriteByte('\n')
	return b.String()
}

func (e *Exporter) LastError() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastError
}

func (e *Exporter) LastSuccess() time.Time {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastSuccess
}

func (e *Exporter) LastPartial() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastPartial
}

func (e *Exporter) ShouldNotifyPartial() (string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastPartial == "" {
		e.lastNotifiedPartial = ""
		return "", false
	}
	if e.lastPartial == e.lastNotifiedPartial {
		return "", false
	}
	e.lastNotifiedPartial = e.lastPartial
	return e.lastPartial, true
}

func (e *Exporter) bumpQueryCounter(zoneID, zoneDomain, status string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	key := strings.Join([]string{zoneID, zoneDomain, status}, "\x00")
	e.counters[key]++
}

func (e *Exporter) snapshotSamples() []Sample {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Sample, len(e.samples))
	copy(out, e.samples)
	return out
}

func (e *Exporter) fallbackUsage(samples []Sample, period, product string, labels map[string]string) UsageNumbers {
	usage, ok := usageFromSamples(samples, period, product, labels)
	if !ok {
		return UsageNumbers{}
	}
	return usage
}

func usageSamples(period, product string, usage UsageNumbers, labels map[string]string) []Sample {
	out := make([]Sample, 0, 5)
	out = append(out, Sample{Metric: "cloudflare_bytes_total_" + period, Labels: withProduct(labels, product), Value: usage.BytesTotal})
	out = append(out, Sample{Metric: "cloudflare_bytes_ingress_" + period, Labels: withProduct(labels, product), Value: usage.BytesIngress})
	out = append(out, Sample{Metric: "cloudflare_bytes_egress_" + period, Labels: withProduct(labels, product), Value: usage.BytesEgress})
	out = append(out, Sample{Metric: "cloudflare_bytes_cached_" + period, Labels: withProduct(labels, product), Value: usage.BytesCached})
	out = append(out, Sample{Metric: "cloudflare_requests_total_" + period, Labels: withProduct(labels, product), Value: usage.RequestsTotal})
	return out
}

func usageFromSamples(samples []Sample, period, product string, labels map[string]string) (UsageNumbers, bool) {
	targetLabels := withProduct(labels, product)
	metrics := map[string]*float64{}
	for i := range samples {
		sample := samples[i]
		if !sameLabels(sample.Labels, targetLabels) {
			continue
		}
		switch sample.Metric {
		case "cloudflare_bytes_total_" + period:
			metrics["bytes_total"] = &sample.Value
		case "cloudflare_bytes_ingress_" + period:
			metrics["bytes_ingress"] = &sample.Value
		case "cloudflare_bytes_egress_" + period:
			metrics["bytes_egress"] = &sample.Value
		case "cloudflare_bytes_cached_" + period:
			metrics["bytes_cached"] = &sample.Value
		case "cloudflare_requests_total_" + period:
			metrics["requests_total"] = &sample.Value
		}
	}
	if len(metrics) == 0 {
		return UsageNumbers{}, false
	}
	return UsageNumbers{
		BytesTotal:    deref(metrics["bytes_total"]),
		BytesIngress:  deref(metrics["bytes_ingress"]),
		BytesEgress:   deref(metrics["bytes_egress"]),
		BytesCached:   deref(metrics["bytes_cached"]),
		RequestsTotal: deref(metrics["requests_total"]),
	}, true
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func sameLabels(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func withProduct(labels map[string]string, product string) map[string]string {
	cloned := cloneLabels(labels)
	cloned["product"] = product
	return cloned
}

func cloneLabels(labels map[string]string) map[string]string {
	cloned := make(map[string]string, len(labels))
	for k, v := range labels {
		cloned[k] = v
	}
	return cloned
}

func previousMonthEquivalent(now time.Time, loc *time.Location) time.Time {
	currentMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	lastMonthStart := currentMonthStart.AddDate(0, -1, 0)
	targetDay := now.Day()
	lastMonthLastDay := time.Date(lastMonthStart.Year(), lastMonthStart.Month()+1, 0, 0, 0, 0, 0, loc).Day()
	if targetDay > lastMonthLastDay {
		targetDay = lastMonthLastDay
	}
	return time.Date(lastMonthStart.Year(), lastMonthStart.Month(), targetDay, now.Hour(), now.Minute(), now.Second(), now.Nanosecond(), loc)
}

func latestClosedMonthRange(now time.Time) (time.Time, time.Time, string, string) {
	closedMonth := now.AddDate(0, -1, 0)
	start := time.Date(closedMonth.Year(), closedMonth.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(closedMonth.Year(), closedMonth.Month()+1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Second)
	return start, end, start.Format("2006-01-02"), end.Format("2006-01-02")
}

func mergeUsage(a, b UsageNumbers) UsageNumbers {
	return UsageNumbers{
		BytesTotal:    a.BytesTotal + b.BytesTotal,
		BytesIngress:  a.BytesIngress + b.BytesIngress,
		BytesEgress:   a.BytesEgress + b.BytesEgress,
		BytesCached:   a.BytesCached + b.BytesCached,
		RequestsTotal: a.RequestsTotal + b.RequestsTotal,
	}
}

func statusLabel(err error) string {
	if err != nil {
		return "error"
	}
	return "success"
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func endOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location())
}

func writeMetricHeader(b *strings.Builder, name, metricType, help string) {
	b.WriteString("# HELP ")
	b.WriteString(name)
	b.WriteByte(' ')
	b.WriteString(help)
	b.WriteByte('\n')
	b.WriteString("# TYPE ")
	b.WriteString(name)
	b.WriteByte(' ')
	b.WriteString(strings.ToLower(metricType))
	b.WriteByte('\n')
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, key := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(key)
		b.WriteString("=\"")
		b.WriteString(escapeLabelValue(labels[key]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func escapeLabelValue(v string) string {
	replacer := strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`)
	return replacer.Replace(v)
}
