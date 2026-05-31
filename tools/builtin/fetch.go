package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/deepact/deepact/artifact"
	"github.com/deepact/deepact/tools"
	"golang.org/x/net/html"
)

const (
	fetchMaxBody     = 1 * 1024 * 1024 // 1MB max body to read
	fetchDigestLines = 500             // max lines returned inline
	fetchDigestBytes = 2000            // small output threshold
)

type FetchTool struct {
	client *http.Client
}

func NewFetchTool() *FetchTool {
	return &FetchTool{
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (t *FetchTool) Spec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        "fetch",
		Description: "Fetch a web page and extract readable text content. Returns line-numbered text extracted from HTML, with metadata (URL, status code, content length). For large pages, full content is stored in the artifact store.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"URL to fetch (http/https)"},"timeout":{"type":"integer","description":"Timeout in seconds (default 30)"}},"required":["url"]}`),
	}
}

type fetchInput struct {
	URL     string `json:"url"`
	Timeout int    `json:"timeout"`
}

func (t *FetchTool) Run(ctx tools.ToolContext, input json.RawMessage) (tools.ToolResultEnvelope, error) {
	var payload fetchInput
	if err := json.Unmarshal(input, &payload); err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("invalid input: %v", err)}, err
	}
	payload.URL = strings.TrimSpace(payload.URL)
	if payload.URL == "" {
		err := errors.New("url is required")
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	// Build client with optional timeout
	client := t.client
	if payload.Timeout > 0 {
		client = &http.Client{
			Timeout: time.Duration(payload.Timeout) * time.Second,
		}
	}

	// Fetch the page
	resp, err := client.Get(payload.URL)
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("fetch %s: %v", payload.URL, err)}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		err := fmt.Errorf("HTTP %d for %s", resp.StatusCode, payload.URL)
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	// Check content type — reject binary/non-HTML
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !isHTMLContentType(ct) {
		err := fmt.Errorf("unsupported content type: %s", ct)
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: err.Error()}, err
	}

	// Read body with limit
	body, err := io.ReadAll(io.LimitReader(resp.Body, fetchMaxBody))
	if err != nil {
		return tools.ToolResultEnvelope{Status: tools.StatusError, Digest: fmt.Sprintf("read body: %v", err)}, err
	}

	// Extract text from HTML
	text := extractText(string(body))
	if text == "" {
		text = "(empty content)"
	}

	// Build metadata header
	header := fmt.Sprintf("URL: %s\nStatus: %d\nContent-Length: %d bytes (extracted %d)\n\n",
		payload.URL, resp.StatusCode, len(body), len(text))

	// Preserve original body in artifact for large fetches
	if len(body) > fetchDigestBytes && ctx.ArtifactDir != "" {
		store, err := artifact.New(ctx.ArtifactDir)
		if err == nil {
			ref, _, _ := store.StoreWithRedaction(body)
			header += fmt.Sprintf("[Full HTML source: %s]\n\n", ref)
		}
	}

	content := header + text

	// Return inline if small, otherwise store in artifact
	return truncateOrStoreFetch(content, ctx.ArtifactDir)
}

// isHTMLContentType checks if the Content-Type is HTML-like.
func isHTMLContentType(ct string) bool {
	lower := strings.ToLower(ct)
	return strings.Contains(lower, "text/html") ||
		strings.Contains(lower, "application/xhtml+xml") ||
		strings.Contains(lower, "text/plain")
}

// extractText parses HTML and extracts readable text content.
// It skips script, style, noscript, and other non-content elements.
// Text nodes are collected with whitespace normalization.
func extractText(htmlContent string) string {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		// Fallback: simple tag stripping
		return stripHTMLTags(htmlContent)
	}

	var buf strings.Builder
	extractNodeText(doc, &buf)
	return strings.TrimSpace(buf.String())
}

var blockElements = map[string]bool{
	"p": true, "div": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "li": true, "blockquote": true,
	"pre": true, "tr": true, "td": true, "th": true,
}

var skipElements = map[string]bool{
	"script": true, "style": true, "noscript": true,
	"head": true, "meta": true, "link": true,
	"svg": true, "iframe": true, "object": true,
}

// extractNodeText recursively extracts text from HTML nodes.
func extractNodeText(n *html.Node, buf *strings.Builder) {
	if n.Type == html.TextNode {
		text := strings.TrimSpace(n.Data)
		if text != "" {
			if buf.Len() > 0 {
				last := buf.String()[buf.Len()-1]
				if last != '\n' && last != ' ' {
					buf.WriteByte(' ')
				}
			}
			buf.WriteString(text)
		}
		return
	}

	if n.Type == html.ElementNode {
		if skipElements[n.Data] {
			return
		}
	}

	// Process children
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		extractNodeText(c, buf)
	}

	// Add newline after block elements
	if n.Type == html.ElementNode && blockElements[n.Data] {
		buf.WriteByte('\n')
	}
}

// stripHTMLTags is a fallback that removes all HTML tags using a simple state machine.
// Only used when HTML parsing fails.
func stripHTMLTags(s string) string {
	var buf strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			buf.WriteRune(r)
		}
	}
	return strings.TrimSpace(buf.String())
}

// truncateOrStoreFetch returns content inline if small, otherwise stores in artifact.
func truncateOrStoreFetch(content, artifactDir string) (tools.ToolResultEnvelope, error) {
	if len(content) <= fetchDigestBytes {
		return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: content}, nil
	}

	lines := strings.Split(content, "\n")
	if len(lines) <= fetchDigestLines {
		return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: content}, nil
	}

	// Try artifact store
	if artifactDir != "" {
		store, err := artifact.New(artifactDir)
		if err == nil {
			ref, _, storeErr := store.StoreWithRedaction([]byte(content))
			if storeErr == nil {
				truncated := strings.Join(lines[:fetchDigestLines], "\n")
				digest := fmt.Sprintf("%s\n[... truncated at %d lines, full content in artifact: %s]", truncated, fetchDigestLines, ref)
				return tools.ToolResultEnvelope{
					Status:      tools.StatusOK,
					Digest:      digest,
					ArtifactRef: ref,
				}, nil
			}
		}
	}

	// Fallback
	truncated := strings.Join(lines[:fetchDigestLines], "\n")
	digest := fmt.Sprintf("%s\n[... truncated at %d lines]", truncated, fetchDigestLines)
	return tools.ToolResultEnvelope{Status: tools.StatusOK, Digest: digest}, nil
}
