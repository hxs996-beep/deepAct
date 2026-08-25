package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/deepact/deepact/artifact"
	"github.com/deepact/deepact/tools"
)

const (
	searchDefaultBaseURL = "https://api.tavily.com"
	searchDefaultResults = 5
	searchMaxResults     = 20
	searchTimeout        = 30 * time.Second
	searchMaxBody        = 1 * 1024 * 1024 // 1MB cap on the response body read
	searchInlineBytes    = 2000            // inline-digest threshold
	searchInlineLines    = 250             // inline-digest line cap
	searchSnippetCap     = 300             // chars kept per-result snippet inline
)

// WebSearchConfig configures the web search tool. All fields are optional;
// empty values fall back to defaults. The API key may be set here or via the
// TAVILY_API_KEY environment variable (used when cfg.APIKey is empty).
type WebSearchConfig struct {
	Provider   string // "tavily" (default)
	APIKey     string
	BaseURL    string // default https://api.tavily.com
	MaxResults int    // default 5, capped at 20
}

type WebSearchTool struct {
	client *http.Client
	cfg    WebSearchConfig
}

func NewWebSearchTool(cfg WebSearchConfig) *WebSearchTool {
	if cfg.Provider == "" {
		cfg.Provider = "tavily"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = searchDefaultBaseURL
	}
	if cfg.MaxResults <= 0 {
		cfg.MaxResults = searchDefaultResults
	}
	if cfg.MaxResults > searchMaxResults {
		cfg.MaxResults = searchMaxResults
	}
	return &WebSearchTool{
		client: &http.Client{Timeout: searchTimeout},
		cfg:    cfg,
	}
}

func (t *WebSearchTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        "web_search",
		Description: "Search the web and return ranked results (title, URL, snippet) for a query. Use this to discover information, docs, references, or latest details you don't already know. Pair with the `fetch` tool to read a specific result's full page. Backed by Tavily; requires an API key (TAVILY_API_KEY or [search].api_key).",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Search query"},"max_results":{"type":"integer","description":"Max results to return (default 5, max 20)"},"search_depth":{"type":"string","description":"basic (default) for fast results, advanced for deeper crawling"}},"required":["query"]}`),
	}
}

type webSearchInput struct {
	Query       string `json:"query"`
	MaxResults  int    `json:"max_results"`
	SearchDepth string `json:"search_depth"`
}

type tavilyResponse struct {
	Query   string         `json:"query"`
	Answer  string         `json:"answer"`
	Results []tavilyResult `json:"results"`
	Error   string         `json:"error"`
}

type tavilyResult struct {
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
}

func (t *WebSearchTool) apiKey() string {
	if t.cfg.APIKey != "" {
		return t.cfg.APIKey
	}
	return os.Getenv("TAVILY_API_KEY")
}

func (t *WebSearchTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload webSearchInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	payload.Query = strings.TrimSpace(payload.Query)
	if payload.Query == "" {
		err := errors.New("query is required")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	apiKey := t.apiKey()
	if apiKey == "" {
		err := errors.New("web_search: no API key. Set TAVILY_API_KEY or configure [search].api_key")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	maxResults := payload.MaxResults
	if maxResults <= 0 {
		maxResults = t.cfg.MaxResults
	}
	if maxResults > searchMaxResults {
		maxResults = searchMaxResults
	}
	depth := payload.SearchDepth
	if depth == "" {
		depth = "basic"
	}

	resp, err := t.search(apiKey, payload.Query, maxResults, depth)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("web_search: %v", err)}, err
	}

	content := formatSearchResults(payload.Query, resp)
	return truncateOrStoreSearch(content, ctx.ArtifactDir)
}

// search performs a Tavily search request and parses the ranked results.
func (t *WebSearchTool) search(apiKey, query string, maxResults int, depth string) (*tavilyResponse, error) {
	body, err := json.Marshal(map[string]interface{}{
		"query":          query,
		"max_results":    maxResults,
		"search_depth":   depth,
		"include_answer": true,
	})
	if err != nil {
		return nil, err
	}

	endpoint := strings.TrimRight(t.cfg.BaseURL, "/") + "/search"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, searchMaxBody))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var parsed tavilyResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	if parsed.Error != "" {
		return nil, fmt.Errorf("tavily: %s", parsed.Error)
	}
	return &parsed, nil
}

// formatSearchResults renders results as ranked plain text.
func formatSearchResults(query string, resp *tavilyResponse) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Query: %s\n\n", query))
	if resp == nil {
		b.WriteString("(no results)\n")
		return b.String()
	}
	if resp.Answer != "" {
		b.WriteString("Answer: " + strings.TrimSpace(resp.Answer) + "\n\n")
	}
	if len(resp.Results) == 0 {
		b.WriteString("(no results)\n")
		return b.String()
	}
	for i, r := range resp.Results {
		b.WriteString(fmt.Sprintf("%d. %s\n", i+1, r.Title))
		if r.URL != "" {
			b.WriteString("   " + r.URL + "\n")
		}
		snippet := r.Content
		if len(snippet) > searchSnippetCap {
			snippet = snippet[:searchSnippetCap] + "…"
		}
		if strings.TrimSpace(snippet) != "" {
			b.WriteString("   " + strings.TrimSpace(snippet) + "\n")
		}
		if r.Score > 0 {
			b.WriteString(fmt.Sprintf("   score: %.2f\n", r.Score))
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// truncateOrStoreSearch returns content inline if small, otherwise stores the
// full text in the artifact store and returns a capped digest.
func truncateOrStoreSearch(content, artifactDir string) (tools.ToolResultEnvelope, error) {
	if len(content) <= searchInlineBytes {
		return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: content}, nil
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= searchInlineLines {
		return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: content}, nil
	}

	truncated := strings.Join(lines[:searchInlineLines], "\n")
	if artifactDir != "" {
		store, err := artifact.New(artifactDir)
		if err == nil {
			ref, _, storeErr := store.StoreWithRedaction([]byte(content))
			if storeErr == nil {
				digest := fmt.Sprintf("%s\n[... truncated at %d lines, full content in artifact: %s]", truncated, searchInlineLines, ref)
				return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: digest, ArtifactRef: ref}, nil
			}
		}
	}
	digest := fmt.Sprintf("%s\n[... truncated at %d lines]", truncated, searchInlineLines)
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: digest}, nil
}
