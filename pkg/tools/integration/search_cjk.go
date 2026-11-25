package integrationtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/sipeed/picoclaw/pkg/config"
)

func extractSogouURL(href string) string {
	match := reSogouRealURL.FindStringSubmatch(href)
	if len(match) < 2 {
		return ""
	}
	decoded, err := url.QueryUnescape(match[1])
	if err != nil {
		return ""
	}
	return decoded
}

var (
	reSogouTitle = regexp.MustCompile(
		`<a\s+class="?resultLink"?\s+href="([^"]+)"[^>]*id="sogou_vr_\d+_\d+"[^>]*>\s*(.*?)\s*</a>`,
	)
	reSogouSnippet = regexp.MustCompile(`<div class="clamp\d*[^"]*">\s*(.*?)\s*</div>`)
	reSogouRealURL = regexp.MustCompile(`url=([^&]+)`)
)

type SogouSearchProvider struct {
	proxy  string
	client *http.Client
}

type GeminiSearchProvider struct {
	apiKey string
	model  string
	proxy  string
	client *http.Client
}

func (p *GeminiSearchProvider) Search(
	ctx context.Context,
	query string,
	count int,
	rangeCode string,
) (string, error) {
	if strings.TrimSpace(p.apiKey) == "" {
		return "", errors.New("no API key provided")
	}
	model := strings.TrimSpace(p.model)
	if model == "" {
		model = "gemini-2.5-flash"
	}

	payload := map[string]any{
		"contents": []map[string]any{{
			"parts": []map[string]string{{"text": query}},
		}},
		"tools": []map[string]any{{"google_search": map[string]any{}}},
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	endpoint := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent",
		url.PathEscape(model),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBuffer(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", p.apiKey)
	req.Header.Set("User-Agent", fmt.Sprintf(userAgentHonest, config.Version))

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gemini search api error (status %d): %s", resp.StatusCode, string(body))
	}

	var searchResp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			GroundingMetadata struct {
				GroundingChunks []struct {
					Web struct {
						URI   string `json:"uri"`
						Title string `json:"title"`
					} `json:"web"`
				} `json:"groundingChunks"`
			} `json:"groundingMetadata"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	if len(searchResp.Candidates) == 0 {
		return fmt.Sprintf("No results for: %s", query), nil
	}

	candidate := searchResp.Candidates[0]
	lines := []string{fmt.Sprintf("Results for: %s (via Gemini Google Search)", query)}
	for _, part := range candidate.Content.Parts {
		if strings.TrimSpace(part.Text) != "" {
			lines = append(lines, strings.TrimSpace(part.Text))
		}
	}
	citationCount := 0
	for _, chunk := range candidate.GroundingMetadata.GroundingChunks {
		if strings.TrimSpace(chunk.Web.URI) == "" {
			continue
		}
		citationCount++
		title := strings.TrimSpace(chunk.Web.Title)
		if title == "" {
			title = chunk.Web.URI
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", citationCount, title, chunk.Web.URI))
		if citationCount >= count {
			break
		}
	}
	return strings.Join(lines, "\n"), nil
}

func (p *SogouSearchProvider) Search(
	ctx context.Context,
	query string,
	count int,
	rangeCode string,
) (string, error) {
	const sogouWAPURL = "https://wap.sogou.com/web/searchList.jsp"

	results := make([]SearchResultItem, 0, count)
	seenURLs := make(map[string]bool)
	maxPages := min(3, (count+1)/2+1)

	for page := 1; page <= maxPages && len(results) < count; page++ {
		params := url.Values{}
		params.Set("keyword", applySogouRangeHint(query, rangeCode))
		params.Set("v", "5")
		params.Set("p", fmt.Sprintf("%d", page))

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, sogouWAPURL+"?"+params.Encode(), nil)
		if err != nil {
			return "", fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("User-Agent", sogouUserAgent)

		resp, err := p.client.Do(req)
		if err != nil {
			return "", fmt.Errorf("request failed: %w", err)
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("failed to read response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("Sogou returned status %d", resp.StatusCode)
		}

		html := string(body)
		if len(html) < 200 {
			break
		}

		matches := reSogouTitle.FindAllStringSubmatch(html, -1)
		for _, match := range matches {
			if len(match) < 3 {
				continue
			}

			title := stripTags(match[2])
			link := extractSogouURL(match[1])
			if title == "" || link == "" || seenURLs[link] {
				continue
			}
			seenURLs[link] = true

			start := strings.Index(html, match[0])
			snippet := ""
			if start >= 0 {
				after := html[start+len(match[0]):]
				if len(after) > 2000 {
					after = after[:2000]
				}
				if snippetMatch := reSogouSnippet.FindStringSubmatch(after); len(snippetMatch) > 1 {
					snippet = stripTags(snippetMatch[1])
				}
			}

			results = append(results, SearchResultItem{
				Title:   title,
				URL:     link,
				Snippet: snippet,
			})
			if len(results) >= count {
				break
			}
		}
	}

	if len(results) == 0 {
		return fmt.Sprintf("No results for: %s", query), nil
	}

	lines := []string{fmt.Sprintf("Results for: %s (via Sogou)", query)}
	for i, item := range results {
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, item.Title, item.URL))
		if item.Snippet != "" {
			lines = append(lines, fmt.Sprintf("   %s", item.Snippet))
		}
	}
	return strings.Join(lines, "\n"), nil
}

type BaiduSearchProvider struct {
	apiKey  string
	baseURL string
	proxy   string
	client  *http.Client
}

func (p *BaiduSearchProvider) Search(
	ctx context.Context,
	query string,
	count int,
	rangeCode string,
) (string, error) {
	if p.apiKey == "" {
		return "", errors.New("no API key provided")
	}

	searchURL := p.baseURL
	if searchURL == "" {
		searchURL = "https://qianfan.baidubce.com/v2/ai_search/web_search"
	}

	payload := map[string]any{
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": query,
			},
		},
		"search_source":        "baidu_search_v2",
		"resource_type_filter": []map[string]any{{"type": "web", "top_k": count}},
	}
	if recencyFilter := mapBaiduRecencyFilter(rangeCode); recencyFilter != "" {
		payload["search_recency_filter"] = recencyFilter
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", searchURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("baidu search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("baidu search API error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		References []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"references"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if len(result.References) == 0 {
		return fmt.Sprintf("No results for: %s", query), nil
	}

	lines := []string{fmt.Sprintf("Results for: %s (via Baidu Search)", query)}
	for i, item := range result.References {
		if i >= count {
			break
		}
		lines = append(lines, fmt.Sprintf("%d. %s\n   %s", i+1, item.Title, item.URL))
		if item.Content != "" {
			lines = append(lines, fmt.Sprintf("   %s", item.Content))
		}
	}

	return strings.Join(lines, "\n"), nil
}
