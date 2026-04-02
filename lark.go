package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type LarkNotifier struct {
	cfg        LarkConfig
	httpClient *http.Client
}

func NewLarkNotifier(cfg LarkConfig) *LarkNotifier {
	if cfg.WebhookURL == "" {
		return nil
	}
	if cfg.Title == "" {
		cfg.Title = "Cloudflare Analytics Exporter Alert"
	}
	return &LarkNotifier{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func (n *LarkNotifier) NotifyCollectionFailure(cfg Config, exporter *Exporter, lastErr error, attempts int) error {
	if n == nil {
		return nil
	}

	failure := &CollectionFailure{}
	if lastErr != nil {
		if typed, ok := lastErr.(*CollectionFailure); ok {
			failure = typed
		} else {
			failure.Err = lastErr
		}
	}

	lines := []string{
		"**状态：** 采集失败",
		fmt.Sprintf("**重试次数：** %d", attempts),
		fmt.Sprintf("**告警时间：** %s", time.Now().Format(time.RFC3339)),
		fmt.Sprintf("**Prometheus 抓取端口：** %s", escapeCardText(cfg.Metrics.ListenAddr)),
		fmt.Sprintf("**配置 Zone：** %s", escapeCardText(joinZoneDomains(cfg.Cloudflare.Zones))),
	}
	if failure.ZoneDomain != "" {
		lines = append(lines, fmt.Sprintf("**失败 Zone：** %s", escapeCardText(failure.ZoneDomain)))
	}
	if failure.Stage != "" {
		lines = append(lines, fmt.Sprintf("**失败阶段：** %s", escapeCardText(localizeStage(failure.Stage))))
	}
	if exporter != nil {
		lastSuccess := exporter.LastSuccess()
		if !lastSuccess.IsZero() {
			lines = append(lines, fmt.Sprintf("**最近成功：** %s", lastSuccess.Format(time.RFC3339)))
		} else {
			lines = append(lines, "**最近成功：** never")
		}
	}

	payload := map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"config": map[string]any{
				"wide_screen_mode": true,
				"enable_forward":   true,
			},
			"header": map[string]any{
				"template": "red",
				"title": map[string]any{
					"tag":     "plain_text",
					"content": n.cfg.Title,
				},
			},
			"elements": buildCardElements(n.cfg, lines, lastErr),
		},
	}

	if n.cfg.Secret != "" {
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		sign, err := larkSign(timestamp, n.cfg.Secret)
		if err != nil {
			return fmt.Errorf("sign lark request: %w", err)
		}
		payload["timestamp"] = timestamp
		payload["sign"] = sign
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal lark payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, n.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create lark request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send lark request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("lark webhook status %s", resp.Status)
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read lark response: %w", err)
	}
	var larkResp struct {
		Code          int    `json:"code"`
		Msg           string `json:"msg"`
		StatusCode    int    `json:"StatusCode"`
		StatusMessage string `json:"StatusMessage"`
	}
	if len(respBody) > 0 {
		if err := json.Unmarshal(respBody, &larkResp); err == nil {
			if larkResp.Code != 0 {
				return fmt.Errorf("lark webhook code %d: %s", larkResp.Code, larkResp.Msg)
			}
			if larkResp.StatusCode != 0 {
				return fmt.Errorf("lark webhook status code %d: %s", larkResp.StatusCode, larkResp.StatusMessage)
			}
		}
	}
	return nil
}

func larkSign(timestamp, secret string) (string, error) {
	stringToSign := timestamp + "\n" + secret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	if _, err := mac.Write([]byte("")); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

func joinZoneDomains(zones []ZoneConfig) string {
	parts := make([]string, 0, len(zones))
	for _, zone := range zones {
		if zone.Domain != "" {
			parts = append(parts, zone.Domain)
			continue
		}
		if zone.Name != "" {
			parts = append(parts, zone.Name)
		}
	}
	return strings.Join(parts, ", ")
}

func buildCardElements(cfg LarkConfig, lines []string, lastErr error) []map[string]any {
	elements := make([]map[string]any, 0, 6)

	if mention := mentionMarkdown(cfg.MentionUserIDs); mention != "" {
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": mention,
			},
		})
	}

	elements = append(elements, map[string]any{
		"tag": "div",
		"text": map[string]any{
			"tag":     "lark_md",
			"content": strings.Join(lines, "\n"),
		},
	})

	if cfg.DashboardURL != "" {
		elements = append(elements, map[string]any{
			"tag": "action",
			"actions": []map[string]any{
				{
					"tag": "button",
					"text": map[string]any{
						"tag":     "plain_text",
						"content": "打开 Grafana",
					},
					"type": "primary",
					"url":  cfg.DashboardURL,
				},
			},
		})
	}

	if lastErr != nil {
		elements = append(elements, map[string]any{
			"tag": "hr",
		})
		elements = append(elements, map[string]any{
			"tag": "div",
			"text": map[string]any{
				"tag":     "lark_md",
				"content": fmt.Sprintf("**错误信息**\n`%s`", escapeCardText(lastErr.Error())),
			},
		})
	}

	return elements
}

func mentionMarkdown(userIDs []string) string {
	parts := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("<at id=%s></at>", userID))
	}
	return strings.Join(parts, " ")
}

func escapeCardText(s string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "`", "'", "\n", " ")
	return replacer.Replace(s)
}

func localizeStage(stage string) string {
	switch stage {
	case "daily_http":
		return "昨日 HTTP 采集"
	case "monthly_http":
		return "本月 HTTP 累计采集"
	case "last_month_http":
		return "上月 HTTP 累计采集"
	case "last_month_to_date_http":
		return "上月同期 HTTP 采集"
	default:
		return stage
	}
}
