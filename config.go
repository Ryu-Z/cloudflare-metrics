package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	Cloudflare CloudflareConfig
	Metrics    MetricsConfig
	Schedule   ScheduleConfig
	Alerting   AlertingConfig
}

type CloudflareConfig struct {
	APIToken  string
	AccountID string
	Zones     []ZoneConfig
}

type ZoneConfig struct {
	ZoneID         string
	SpectrumZoneID string
	Name           string
	Domain         string
}

type MetricsConfig struct {
	ListenAddr string
}

type ScheduleConfig struct {
	Timezone        string
	IntervalMinutes int
	Daily           DailySchedule
	Retry           RetryConfig
}

type DailySchedule struct {
	Hour   int
	Minute int
}

type RetryConfig struct {
	MaxAttempts  int
	DelaySeconds int
}

type AlertingConfig struct {
	Lark LarkConfig
}

type LarkConfig struct {
	WebhookURL     string
	Secret         string
	Title          string
	DashboardURL   string
	MentionUserIDs []string
}

func LoadConfig(path string) (Config, error) {
	loadDotEnv(filepath.Join(filepath.Dir(path), ".env"))

	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()

	cfg := Config{
		Metrics: MetricsConfig{ListenAddr: ":9589"},
		Schedule: ScheduleConfig{
			Timezone:        "Local",
			IntervalMinutes: 30,
			Daily: DailySchedule{
				Hour:   1,
				Minute: 5,
			},
			Retry: RetryConfig{
				MaxAttempts:  5,
				DelaySeconds: 300,
			},
		},
		Alerting: AlertingConfig{
			Lark: LarkConfig{
				Title: "Cloudflare Analytics Exporter Alert",
			},
		},
	}

	var section string
	var subsection string
	var currentZone *ZoneConfig

	scanner := bufio.NewScanner(file)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		raw := stripComment(scanner.Text())
		if strings.TrimSpace(raw) == "" {
			continue
		}

		indent := leadingSpaces(raw)
		line := strings.TrimSpace(raw)

		switch line {
		case "cloudflare:":
			if currentZone != nil {
				cfg.Cloudflare.Zones = append(cfg.Cloudflare.Zones, *currentZone)
				currentZone = nil
			}
			section = "cloudflare"
			subsection = ""
			continue
		case "metrics:":
			if currentZone != nil {
				cfg.Cloudflare.Zones = append(cfg.Cloudflare.Zones, *currentZone)
				currentZone = nil
			}
			section = "metrics"
			subsection = ""
			continue
		case "schedule:":
			if currentZone != nil {
				cfg.Cloudflare.Zones = append(cfg.Cloudflare.Zones, *currentZone)
				currentZone = nil
			}
			section = "schedule"
			subsection = ""
			continue
		case "alerting:":
			if currentZone != nil {
				cfg.Cloudflare.Zones = append(cfg.Cloudflare.Zones, *currentZone)
				currentZone = nil
			}
			section = "alerting"
			subsection = ""
			continue
		case "zones:":
			subsection = "zones"
			continue
		case "daily:":
			subsection = "daily"
			continue
		case "retry:":
			subsection = "retry"
			continue
		case "lark:":
			subsection = "lark"
			continue
		}

		if section == "cloudflare" && subsection == "zones" && strings.HasPrefix(line, "- ") {
			if currentZone != nil {
				cfg.Cloudflare.Zones = append(cfg.Cloudflare.Zones, *currentZone)
			}
			currentZone = &ZoneConfig{}
			rest := strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if rest != "" {
				key, value, err := splitKV(rest)
				if err != nil {
					return Config{}, fmt.Errorf("config line %d: %w", lineNo, err)
				}
				if err := assignZoneField(currentZone, key, value); err != nil {
					return Config{}, fmt.Errorf("config line %d: %w", lineNo, err)
				}
			}
			continue
		}

		key, value, err := splitKV(line)
		if err != nil {
			return Config{}, fmt.Errorf("config line %d: %w", lineNo, err)
		}

		if currentZone != nil && section == "cloudflare" && subsection == "zones" && indent >= 4 {
			if err := assignZoneField(currentZone, key, value); err != nil {
				return Config{}, fmt.Errorf("config line %d: %w", lineNo, err)
			}
			continue
		}

		switch section {
		case "cloudflare":
			switch key {
			case "api_token":
				cfg.Cloudflare.APIToken = value
			case "account_id":
				cfg.Cloudflare.AccountID = value
			default:
				return Config{}, fmt.Errorf("unsupported cloudflare key %q", key)
			}
		case "metrics":
			switch key {
			case "listen_addr":
				cfg.Metrics.ListenAddr = value
			default:
				return Config{}, fmt.Errorf("unsupported metrics key %q", key)
			}
		case "schedule":
			switch subsection {
			case "":
				if err := assignScheduleField(&cfg.Schedule, key, value); err != nil {
					return Config{}, fmt.Errorf("config line %d: %w", lineNo, err)
				}
			case "daily":
				if err := assignDailyField(&cfg.Schedule.Daily, key, value); err != nil {
					return Config{}, fmt.Errorf("config line %d: %w", lineNo, err)
				}
			case "retry":
				if err := assignRetryField(&cfg.Schedule.Retry, key, value); err != nil {
					return Config{}, fmt.Errorf("config line %d: %w", lineNo, err)
				}
			default:
				return Config{}, fmt.Errorf("unsupported schedule subsection %q", subsection)
			}
		case "alerting":
			switch subsection {
			case "lark":
				if err := assignLarkField(&cfg.Alerting.Lark, key, value); err != nil {
					return Config{}, fmt.Errorf("config line %d: %w", lineNo, err)
				}
			default:
				return Config{}, fmt.Errorf("unsupported alerting subsection %q", subsection)
			}
		default:
			return Config{}, fmt.Errorf("key %q found outside a supported section", key)
		}
	}
	if err := scanner.Err(); err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if currentZone != nil {
		cfg.Cloudflare.Zones = append(cfg.Cloudflare.Zones, *currentZone)
	}

	applyEnvOverrides(&cfg)

	if cfg.Cloudflare.APIToken == "" {
		return Config{}, fmt.Errorf("cloudflare.api_token is required")
	}
	if len(cfg.Cloudflare.Zones) == 0 {
		return Config{}, fmt.Errorf("at least one cloudflare zone is required")
	}
	for i := range cfg.Cloudflare.Zones {
		if cfg.Cloudflare.Zones[i].ZoneID == "" {
			return Config{}, fmt.Errorf("cloudflare.zones[%d].zone_id is required", i)
		}
		if cfg.Cloudflare.Zones[i].SpectrumZoneID == "" {
			cfg.Cloudflare.Zones[i].SpectrumZoneID = cfg.Cloudflare.Zones[i].ZoneID
		}
		if cfg.Cloudflare.Zones[i].Domain == "" {
			cfg.Cloudflare.Zones[i].Domain = cfg.Cloudflare.Zones[i].Name
		}
		if cfg.Cloudflare.Zones[i].Name == "" {
			cfg.Cloudflare.Zones[i].Name = cfg.Cloudflare.Zones[i].Domain
		}
	}

	return cfg, nil
}

func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(stripComment(scanner.Text()))
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		value = strings.Trim(value, `"`)
		value = strings.Trim(value, `'`)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, value)
	}
}

func applyEnvOverrides(cfg *Config) {
	if value := os.Getenv("CF_API_TOKEN"); value != "" {
		cfg.Cloudflare.APIToken = value
	}
	if value := os.Getenv("CF_ACCOUNT_ID"); value != "" {
		cfg.Cloudflare.AccountID = value
	}
	if value := os.Getenv("CF_ZONE_ID"); value != "" {
		ensureZone(cfg)
		cfg.Cloudflare.Zones[0].ZoneID = value
	}
	if value := os.Getenv("CF_SPECTRUM_ZONE_ID"); value != "" {
		ensureZone(cfg)
		cfg.Cloudflare.Zones[0].SpectrumZoneID = value
	}
	if value := os.Getenv("CF_ZONE_NAME"); value != "" {
		ensureZone(cfg)
		cfg.Cloudflare.Zones[0].Name = value
	}
	if value := os.Getenv("CF_ZONE_DOMAIN"); value != "" {
		ensureZone(cfg)
		cfg.Cloudflare.Zones[0].Domain = value
	}
	if value := os.Getenv("LARK_WEBHOOK_URL"); value != "" {
		cfg.Alerting.Lark.WebhookURL = value
	}
	if value := os.Getenv("LARK_SECRET"); value != "" {
		cfg.Alerting.Lark.Secret = value
	}
	if value := os.Getenv("LARK_TITLE"); value != "" {
		cfg.Alerting.Lark.Title = value
	}
	if value := os.Getenv("LARK_DASHBOARD_URL"); value != "" {
		cfg.Alerting.Lark.DashboardURL = value
	}
	if value := os.Getenv("LARK_MENTION_USER_IDS"); value != "" {
		cfg.Alerting.Lark.MentionUserIDs = splitCommaSeparated(value)
	}
}

func ensureZone(cfg *Config) {
	if len(cfg.Cloudflare.Zones) == 0 {
		cfg.Cloudflare.Zones = append(cfg.Cloudflare.Zones, ZoneConfig{})
	}
}

func splitKV(line string) (string, string, error) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid key/value line %q", line)
	}
	return strings.TrimSpace(parts[0]), trimYAMLValue(parts[1]), nil
}

func trimYAMLValue(v string) string {
	s := strings.TrimSpace(v)
	s = strings.Trim(s, `"`)
	s = strings.Trim(s, `'`)
	return s
}

func assignZoneField(zone *ZoneConfig, key, value string) error {
	switch key {
	case "zone_id":
		zone.ZoneID = value
	case "spectrum_zone_id":
		zone.SpectrumZoneID = value
	case "name":
		zone.Name = value
	case "domain":
		zone.Domain = value
	default:
		return fmt.Errorf("unsupported zone key %q", key)
	}
	return nil
}

func assignDailyField(daily *DailySchedule, key, value string) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("invalid integer for daily.%s: %w", key, err)
	}
	switch key {
	case "hour":
		daily.Hour = n
	case "minute":
		daily.Minute = n
	default:
		return fmt.Errorf("unsupported daily key %q", key)
	}
	return nil
}

func assignScheduleField(schedule *ScheduleConfig, key, value string) error {
	switch key {
	case "timezone":
		schedule.Timezone = value
		return nil
	case "interval_minutes":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer for schedule.%s: %w", key, err)
		}
		schedule.IntervalMinutes = n
		return nil
	default:
		return fmt.Errorf("unsupported schedule key %q", key)
	}
}

func assignRetryField(retry *RetryConfig, key, value string) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("invalid integer for retry.%s: %w", key, err)
	}
	switch key {
	case "max_attempts":
		retry.MaxAttempts = n
	case "delay_seconds":
		retry.DelaySeconds = n
	default:
		return fmt.Errorf("unsupported retry key %q", key)
	}
	return nil
}

func assignLarkField(lark *LarkConfig, key, value string) error {
	switch key {
	case "webhook_url":
		lark.WebhookURL = value
	case "secret":
		lark.Secret = value
	case "title":
		lark.Title = value
	case "dashboard_url":
		lark.DashboardURL = value
	case "mention_user_ids":
		lark.MentionUserIDs = splitCommaSeparated(value)
	default:
		return fmt.Errorf("unsupported lark key %q", key)
	}
	return nil
}

func splitCommaSeparated(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func stripComment(line string) string {
	inSingle := false
	inDouble := false
	for i, r := range line {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}

func leadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r != ' ' {
			break
		}
		n++
	}
	return n
}
