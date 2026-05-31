package artifact

import (
	"bytes"
	"regexp"
)

// Patterns for sensitive data detection.
// Order matters: more specific patterns first to avoid partial matches.
var sensitivePatterns = []struct {
	name    string
	pattern *regexp.Regexp
	replace []byte
}{
	{
		name:    "openai-api-key",
		pattern: regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),
		replace: []byte(`sk-[REDACTED-API-KEY]`),
	},
	{
		name:    "deepseek-api-key",
		pattern: regexp.MustCompile(`(?i)(deepseek[_-]?api[_-]?key\s*[:=]\s*)(sk-[A-Za-z0-9]+)`),
		replace: []byte(`$1sk-[REDACTED]`),
	},
	{
		name:    "bearer-token",
		pattern: regexp.MustCompile(`(?i)(Authorization:\s*Bearer\s+)[A-Za-z0-9._\-+/]+`),
		replace: []byte(`$1[REDACTED-BEARER-TOKEN]`),
	},
	{
		name:    "generic-api-key-header",
		pattern: regexp.MustCompile(`(?i)(X-API-Key:\s*)[A-Za-z0-9._\-+/]+`),
		replace: []byte(`$1[REDACTED]`),
	},
	{
		name:    "password-field",
		pattern: regexp.MustCompile(`(?i)(password\s*[:=]\s*)(?:["']?)[^\s"']+`),
		replace: []byte(`$1[REDACTED]`),
	},
	{
		name:    "secret-field",
		pattern: regexp.MustCompile(`(?i)(secret\s*[:=]\s*)(?:["']?)[^\s"']+`),
		replace: []byte(`$1[REDACTED]`),
	},
	{
		name:    "token-field",
		pattern: regexp.MustCompile(`(?i)(token\s*[:=]\s*)(?:["']?)[^\s"']+`),
		replace: []byte(`$1[REDACTED]`),
	},
	{
		name:    "private-key",
		pattern: regexp.MustCompile(`-----BEGIN\s+(?:RSA\s+)?PRIVATE\s+KEY-----[\s\S]+?-----END\s+(?:RSA\s+)?PRIVATE\s+KEY-----`),
		replace: []byte(`[REDACTED-PRIVATE-KEY]`),
	},
	{
		name:    "ssh-private-key",
		pattern: regexp.MustCompile(`-----BEGIN\s+OPENSSH\s+PRIVATE\s+KEY-----[\s\S]+?-----END\s+OPENSSH\s+PRIVATE\s+KEY-----`),
		replace: []byte(`[REDACTED-SSH-PRIVATE-KEY]`),
	},
	{
		name:    "jwt-token",
		pattern: regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
		replace: []byte(`[REDACTED-JWT-TOKEN]`),
	},
	{
		name:    "aws-access-key",
		pattern: regexp.MustCompile(`(AKIA[0-9A-Z]{16})`),
		replace: []byte(`[REDACTED-AWS-KEY]`),
	},
	{
		name:    "github-pat",
		pattern: regexp.MustCompile(`ghp_[A-Za-z0-9]{36,}`),
		replace: []byte(`ghp_[REDACTED-GITHUB-PAT]`),
	},
	{
		name:    "gitlab-pat",
		pattern: regexp.MustCompile(`glpat-[A-Za-z0-9\-_]{20,}`),
		replace: []byte(`glpat-[REDACTED-GITLAB-PAT]`),
	},
	{
		name:    "npm-token",
		pattern: regexp.MustCompile(`npm_[A-Za-z0-9]{36,}`),
		replace: []byte(`npm_[REDACTED-NPM-TOKEN]`),
	},
	{
		name:    "slack-token",
		pattern: regexp.MustCompile(`xox[bapsr]-[A-Za-z0-9\-]{10,}`),
		replace: []byte(`[REDACTED-SLACK-TOKEN]`),
	},
	{
		name:    "generic-dsn",
		pattern: regexp.MustCompile(`(?i)([a-z]+://)[^:@\s]+:[^@\s]+@`),
		replace: []byte(`$1[REDACTED]:[REDACTED]@`),
	},
}

// RedactSensitive scans data for known sensitive patterns and replaces them with markers.
// Returns a new byte slice; the original is not modified.
func RedactSensitive(data []byte) []byte {
	result := data
	for _, sp := range sensitivePatterns {
		result = sp.pattern.ReplaceAll(result, sp.replace)
	}
	return result
}

// ContainsSensitive checks whether data matches any sensitive pattern.
func ContainsSensitive(data []byte) bool {
	for _, sp := range sensitivePatterns {
		if sp.pattern.Match(data) {
			return true
		}
	}
	return false
}

// StoreWithRedaction stores data after redacting sensitive information.
// Returns the artifact reference and whether any redaction occurred.
func (s *Store) StoreWithRedaction(data []byte) (ref string, redacted bool, err error) {
	redactedData := RedactSensitive(data)
	ref, err = s.Store(redactedData)
	if err != nil {
		return "", false, err
	}
	return ref, !bytes.Equal(data, redactedData), nil
}
