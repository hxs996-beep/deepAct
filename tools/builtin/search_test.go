package builtin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/deepact/deepact/tools"
)

// searchTestServer returns a handler that validates the universal request shape
// (method, auth header, content type) and returns the canned Tavily body. Query
// payload fields are verified separately in TestWebSearch_RequestPayload.
func searchTestServer(t *testing.T, status int, respBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer tvly-test" {
			t.Errorf("Authorization = %q, want 'Bearer tvly-test'", r.Header.Get("Authorization"))
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		w.WriteHeader(status)
		w.Write([]byte(respBody))
	}))
}

func TestWebSearch_RequestPayload(t *testing.T) {
	var got map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(200)
		w.Write([]byte(`{"query":"x","results":[]}`))
	}))
	defer ts.Close()

	tool := NewWebSearchTool(WebSearchConfig{APIKey: "tvly-test", BaseURL: ts.URL})
	_, err := tool.Run(tools.ToolContext{}, mustJSON(t, map[string]interface{}{
		"query": "golang context", "max_results": 12, "search_depth": "advanced",
	}))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got["query"] != "golang context" {
		t.Errorf("query = %v, want golang context", got["query"])
	}
	if got["max_results"] != float64(12) {
		t.Errorf("max_results = %v, want 12", got["max_results"])
	}
	if got["search_depth"] != "advanced" {
		t.Errorf("search_depth = %v, want advanced", got["search_depth"])
	}
	if _, ok := got["include_answer"]; !ok {
		t.Errorf("expected include_answer in request: %v", got)
	}
}

func TestWebSearch_ValidQuery(t *testing.T) {
	ts := searchTestServer(t, 200, `{
		"query": "golang context",
		"answer": "context is for cancellation",
		"results": [
			{"title":"Go context docs","url":"https://pkg.go.dev/context","content":"Package context defines the Context type, which carries deadlines, cancelation signals...","score":0.95},
			{"title":"A blog post","url":"https://example.com/post","content":"deep dive","score":0.8}
		]
	}`)
	defer ts.Close()

	tool := NewWebSearchTool(WebSearchConfig{APIKey: "tvly-test", BaseURL: ts.URL})
	result, err := tool.Run(tools.ToolContext{}, mustJSON(t, map[string]interface{}{
		"query": "golang context",
	}))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Status != tools.StatusOK {
		t.Fatalf("status = %q, digest: %s", result.Status, result.Digest)
	}
	for _, want := range []string{"Query: golang context", "Go context docs", "https://pkg.go.dev/context", "score: 0.95"} {
		if !strings.Contains(result.Digest, want) {
			t.Errorf("digest missing %q:\n%s", want, result.Digest)
		}
	}
}

func TestWebSearch_NoAPIKey(t *testing.T) {
	tool := NewWebSearchTool(WebSearchConfig{APIKey: ""}) // no env set in test
	t.Setenv("TAVILY_API_KEY", "")
	result, err := tool.Run(tools.ToolContext{}, mustJSON(t, map[string]interface{}{"query": "x"}))
	if err == nil {
		t.Fatal("expected error when no API key")
	}
	if result.Status != tools.StatusError {
		t.Errorf("status = %q, want error", result.Status)
	}
	if !strings.Contains(result.Digest, "API key") {
		t.Errorf("digest should mention missing API key: %s", result.Digest)
	}
}

func TestWebSearch_QueryRequired(t *testing.T) {
	tool := NewWebSearchTool(WebSearchConfig{APIKey: "tvly-test"})
	result, err := tool.Run(tools.ToolContext{}, mustJSON(t, map[string]interface{}{"query": "  "}))
	if err == nil {
		t.Fatal("expected error for empty query")
	}
	if result.Status != tools.StatusError {
		t.Errorf("status = %q, want error", result.Status)
	}
}

func TestWebSearch_HTTPError(t *testing.T) {
	ts := searchTestServer(t, 500, `{"error":"boom"}`)
	defer ts.Close()
	tool := NewWebSearchTool(WebSearchConfig{APIKey: "tvly-test", BaseURL: ts.URL})
	result, err := tool.Run(tools.ToolContext{}, mustJSON(t, map[string]interface{}{"query": "x"}))
	if err == nil {
		t.Fatal("expected error for HTTP 500")
	}
	if !strings.Contains(result.Digest, "HTTP 500") {
		t.Errorf("digest should mention HTTP 500: %s", result.Digest)
	}
}

func TestWebSearch_TavilyAPIError(t *testing.T) {
	ts := searchTestServer(t, 200, `{"error":"rate limit exceeded"}`)
	defer ts.Close()
	tool := NewWebSearchTool(WebSearchConfig{APIKey: "tvly-test", BaseURL: ts.URL})
	_, err := tool.Run(tools.ToolContext{}, mustJSON(t, map[string]interface{}{"query": "x"}))
	if err == nil {
		t.Fatal("expected error for tavily-level error")
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Errorf("err should mention tavily error: %v", err)
	}
}

func TestWebSearch_NoResults(t *testing.T) {
	ts := searchTestServer(t, 200, `{"query":"x","results":[]}`)
	defer ts.Close()
	tool := NewWebSearchTool(WebSearchConfig{APIKey: "tvly-test", BaseURL: ts.URL})
	result, err := tool.Run(tools.ToolContext{}, mustJSON(t, map[string]interface{}{"query": "x"}))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !strings.Contains(result.Digest, "(no results)") {
		t.Errorf("digest should say no results: %s", result.Digest)
	}
}

func TestFormatSearchResults_SnippetTruncated(t *testing.T) {
	long := strings.Repeat("a", searchSnippetCap+100)
	resp := &tavilyResponse{
		Query:  "q",
		Answer: "answer text",
		Results: []tavilyResult{
			{Title: "T", URL: "https://x", Content: long, Score: 0.9},
		},
	}
	out := formatSearchResults("q", resp)
	if !strings.Contains(out, "answer text") {
		t.Errorf("output should include answer: %s", out)
	}
	if strings.Contains(out, strings.Repeat("a", searchSnippetCap+50)) {
		t.Errorf("snippet should be truncated to searchSnippetCap chars")
	}
}

func TestWebSearch_ConfigDefaultsApplied(t *testing.T) {
	tool := NewWebSearchTool(WebSearchConfig{APIKey: "k"})
	if tool.cfg.BaseURL != searchDefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", tool.cfg.BaseURL, searchDefaultBaseURL)
	}
	if tool.cfg.MaxResults != searchDefaultResults {
		t.Errorf("MaxResults = %d, want %d", tool.cfg.MaxResults, searchDefaultResults)
	}
	if tool.cfg.Provider != "tavily" {
		t.Errorf("Provider = %q, want tavily", tool.cfg.Provider)
	}

	// Cap exceeds the max.
	tooBig := NewWebSearchTool(WebSearchConfig{APIKey: "k", MaxResults: 999})
	if tooBig.cfg.MaxResults != searchMaxResults {
		t.Errorf("MaxResults capped = %d, want %d", tooBig.cfg.MaxResults, searchMaxResults)
	}
}

func mustJSON(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
