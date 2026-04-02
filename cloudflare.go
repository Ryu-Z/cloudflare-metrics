package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const graphQLEndpoint = "https://api.cloudflare.com/client/v4/graphql"
const spectrumByTimeTemplate = "https://api.cloudflare.com/client/v4/zones/%s/spectrum/analytics/events/bytime"

type CloudflareClient struct {
	httpClient *http.Client
	apiToken   string
}

type UsageNumbers struct {
	BytesTotal    float64
	BytesIngress  float64
	BytesEgress   float64
	BytesCached   float64
	RequestsTotal float64
}

func NewCloudflareClient(apiToken string) *CloudflareClient {
	return &CloudflareClient{
		httpClient: &http.Client{Timeout: 45 * time.Second},
		apiToken:   apiToken,
	}
}

func (c *CloudflareClient) FetchHTTPUsage(ctx context.Context, zoneID string, startDate, endDate string) (UsageNumbers, error) {
	query := `
query HTTPUsage($zoneTag: String!, $startDate: Date!, $endDate: Date!) {
  viewer {
    zones(filter: {zoneTag: $zoneTag}) {
      httpRequests1dGroups(
        limit: 10000
        filter: {date_geq: $startDate, date_leq: $endDate}
      ) {
        sum {
          bytes
          cachedBytes
          requests
          threats
        }
      }
    }
  }
}`

	payload := map[string]any{
		"query": query,
		"variables": map[string]any{
			"zoneTag":   zoneID,
			"startDate": startDate,
			"endDate":   endDate,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return UsageNumbers{}, fmt.Errorf("marshal graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphQLEndpoint, bytes.NewReader(body))
	if err != nil {
		return UsageNumbers{}, fmt.Errorf("create graphql request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return UsageNumbers{}, fmt.Errorf("execute graphql request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return UsageNumbers{}, fmt.Errorf("read graphql response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return UsageNumbers{}, fmt.Errorf("graphql status %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var graphResp struct {
		Data struct {
			Viewer struct {
				Zones []struct {
					HTTPRequests1dGroups []struct {
						Sum struct {
							Bytes       float64 `json:"bytes"`
							CachedBytes float64 `json:"cachedBytes"`
							Requests    float64 `json:"requests"`
							Threats     float64 `json:"threats"`
						} `json:"sum"`
					} `json:"httpRequests1dGroups"`
				} `json:"zones"`
			} `json:"viewer"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &graphResp); err != nil {
		return UsageNumbers{}, fmt.Errorf("decode graphql response: %w", err)
	}
	if len(graphResp.Errors) > 0 {
		return UsageNumbers{}, fmt.Errorf("graphql error: %s", graphResp.Errors[0].Message)
	}
	if len(graphResp.Data.Viewer.Zones) == 0 {
		return UsageNumbers{}, nil
	}

	var usage UsageNumbers
	for _, group := range graphResp.Data.Viewer.Zones[0].HTTPRequests1dGroups {
		usage.BytesTotal += group.Sum.Bytes
		usage.BytesCached += group.Sum.CachedBytes
		usage.RequestsTotal += group.Sum.Requests
	}
	return usage, nil
}

func (c *CloudflareClient) FetchSpectrumUsage(ctx context.Context, zoneID string, since, until time.Time) (UsageNumbers, error) {
	endpoint := fmt.Sprintf(spectrumByTimeTemplate, zoneID)
	values := url.Values{}
	values.Set("metrics", "count,bytesIngress,bytesEgress")
	values.Set("since", since.UTC().Format(time.RFC3339))
	values.Set("until", until.UTC().Format(time.RFC3339))
	values.Set("time_delta", "day")
	values.Set("limit", "100000")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return UsageNumbers{}, fmt.Errorf("create spectrum request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return UsageNumbers{}, fmt.Errorf("execute spectrum request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return UsageNumbers{}, fmt.Errorf("read spectrum response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return UsageNumbers{}, fmt.Errorf("spectrum status %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var spectrumResp struct {
		Success bool `json:"success"`
		Result  struct {
			Rows   int `json:"rows"`
			Totals struct {
				BytesIngress float64 `json:"bytesIngress"`
				BytesEgress  float64 `json:"bytesEgress"`
				Count        float64 `json:"count"`
			} `json:"totals"`
		} `json:"result"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &spectrumResp); err != nil {
		return UsageNumbers{}, fmt.Errorf("decode spectrum response: %w", err)
	}
	if len(spectrumResp.Errors) > 0 {
		return UsageNumbers{}, fmt.Errorf("spectrum error: %s", spectrumResp.Errors[0].Message)
	}
	if !spectrumResp.Success {
		return UsageNumbers{}, fmt.Errorf("spectrum request was not successful")
	}

	usage := UsageNumbers{
		BytesIngress:  spectrumResp.Result.Totals.BytesIngress,
		BytesEgress:   spectrumResp.Result.Totals.BytesEgress,
		BytesTotal:    spectrumResp.Result.Totals.BytesIngress + spectrumResp.Result.Totals.BytesEgress,
		RequestsTotal: spectrumResp.Result.Totals.Count,
	}
	return usage, nil
}
