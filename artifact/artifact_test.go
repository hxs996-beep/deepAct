package artifact

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// --- Store tests ---

func TestNew(t *testing.T) {
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "artifacts"))
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil Store")
	}
	if s.BaseDir() == "" {
		t.Error("BaseDir should not be empty")
	}
}

func TestStoreAndLoad(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(filepath.Join(dir, "artifacts"))

	data := []byte("hello world")
	ref, err := s.Store(data)
	if err != nil {
		t.Fatalf("Store() unexpected error: %v", err)
	}

	if len(ref) != 64+7 { // "sha256:" + 64 hex
		t.Errorf("ref length = %d, want 71", len(ref))
	}

	loaded, err := s.Load(ref)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if !bytes.Equal(loaded, data) {
		t.Errorf("loaded data = %q, want %q", loaded, data)
	}
}

func TestStore_DedupByContent(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(filepath.Join(dir, "artifacts"))

	data := []byte("dedup me")
	ref1, err := s.Store(data)
	if err != nil {
		t.Fatalf("first Store(): %v", err)
	}

	ref2, err := s.Store(data)
	if err != nil {
		t.Fatalf("second Store(): %v", err)
	}

	if ref1 != ref2 {
		t.Errorf("same content should produce same ref: %q vs %q", ref1, ref2)
	}
}

func TestLoad_NotFound(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(filepath.Join(dir, "artifacts"))

	_, err := s.Load("sha256:0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected error for nonexistent artifact")
	}
}

func TestLoad_InvalidRef(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(filepath.Join(dir, "artifacts"))

	_, err := s.Load("invalid-ref")
	if err == nil {
		t.Fatal("expected error for invalid ref")
	}
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(filepath.Join(dir, "artifacts"))

	ref, _ := s.Store([]byte("exists test"))
	if !s.Exists(ref) {
		t.Error("Exists() should return true for stored artifact")
	}

	if s.Exists("sha256:0000000000000000000000000000000000000000000000000000000000000000") {
		t.Error("Exists() should return false for nonexistent artifact")
	}

	if s.Exists("invalid") {
		t.Error("Exists() should return false for invalid ref")
	}
}

func TestDelete(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(filepath.Join(dir, "artifacts"))

	ref, _ := s.Store([]byte("delete me"))
	if err := s.Delete(ref); err != nil {
		t.Fatalf("Delete(): %v", err)
	}

	if s.Exists(ref) {
		t.Error("artifact should not exist after delete")
	}
}

func TestDelete_NotFound(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(filepath.Join(dir, "artifacts"))

	// Deleting nonexistent artifact should be idempotent (no error)
	if err := s.Delete("sha256:0000000000000000000000000000000000000000000000000000000000000000"); err != nil {
		t.Fatalf("Delete() of nonexistent should not error: %v", err)
	}
}

func TestDelete_InvalidRef(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(filepath.Join(dir, "artifacts"))

	if err := s.Delete("bad-ref"); err == nil {
		t.Fatal("expected error for invalid ref")
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(filepath.Join(dir, "artifacts"))

	// Empty store
	list, err := s.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 artifacts, got %d", len(list))
	}

	// Store some artifacts
	s.Store([]byte("first"))
	s.Store([]byte("second"))

	list, err = s.List()
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 artifacts, got %d", len(list))
	}
}

func TestBaseDir(t *testing.T) {
	dir := t.TempDir()
	baseDir := filepath.Join(dir, "artifacts")
	s, _ := New(baseDir)
	if s.BaseDir() != baseDir {
		t.Errorf("BaseDir = %q, want %q", s.BaseDir(), baseDir)
	}
}

func TestParseRef(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		wantOK  bool
		wantHex string
	}{
		{
			name:   "valid",
			ref:    "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			wantOK: true,
			wantHex: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
		{
			name:   "missing prefix",
			ref:    "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			wantOK: false,
		},
		{
			name:   "wrong prefix",
			ref:    "md5:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			wantOK: false,
		},
		{
			name:   "too short",
			ref:    "sha256:abc",
			wantOK: false,
		},
		{
			name:   "invalid hex",
			ref:    "sha256:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
			wantOK: false,
		},
		{
			name:   "empty",
			ref:    "",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		hex, err := parseRef(tt.ref)
		if (err == nil) != tt.wantOK {
			t.Errorf("%s: parseRef() err = %v, wantOK=%v", tt.name, err, tt.wantOK)
		}
		if err == nil && hex != tt.wantHex {
			t.Errorf("%s: hex = %q, want %q", tt.name, hex, tt.wantHex)
		}
	}
}

// --- Redact tests ---

func TestRedactSensitive_OpenAIKey(t *testing.T) {
	input := []byte(`api_key = "sk-abc123xyz456def789ghi"`)
	output := RedactSensitive(input)
	if bytes.Contains(output, []byte("sk-abc123xyz456def789ghi")) {
		t.Error("OpenAI API key should be redacted")
	}
	if !bytes.Contains(output, []byte("sk-[REDACTED-API-KEY]")) {
		t.Error("should contain redaction marker")
	}
}

func TestRedactSensitive_DeepSeekKey(t *testing.T) {
	input := []byte(`deepseek_api_key = sk-abcdefghijklmnop`)
	output := RedactSensitive(input)
	if bytes.Contains(output, []byte("sk-abcdefghijklmnop")) {
		t.Error("DeepSeek API key should be redacted")
	}
	if !bytes.Contains(output, []byte("[REDACTED]")) {
		t.Error("output should contain redaction marker")
	}
}

func TestRedactSensitive_PrivateKey(t *testing.T) {
	input := []byte(`-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA0Oc
-----END RSA PRIVATE KEY-----`)
	output := RedactSensitive(input)
	if !bytes.Contains(output, []byte("[REDACTED-PRIVATE-KEY]")) {
		t.Error("private key should be redacted")
	}
}

func TestRedactSensitive_JWT(t *testing.T) {
	// JWT is best matched in free text, not in token= format — the token-field
	// pattern runs first and would catch it (replacing with [REDACTED]).
	// This test verifies standalone JWT detection.
	input := []byte(`token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNvrPwTqGEMnBAMX7g`)
	output := RedactSensitive(input)
	if bytes.Contains(output, []byte("eyJhbGciOiJIUzI1NiJ9")) {
		t.Error("JWT token should be redacted")
	}
	if !bytes.Contains(output, []byte("[REDACTED-JWT-TOKEN]")) {
		t.Error("output should contain JWT redaction marker")
	}
}

func TestRedactSensitive_DSN(t *testing.T) {
	input := []byte(`mysql://user:secret123@localhost:3306/db`)
	output := RedactSensitive(input)
	if bytes.Contains(output, []byte("secret123")) {
		t.Error("password in DSN should be redacted")
	}
}

func TestRedactSensitive_NoChangeForCleanData(t *testing.T) {
	input := []byte("this is clean data with no secrets")
	output := RedactSensitive(input)
	if !bytes.Equal(input, output) {
		t.Error("clean data should not be modified")
	}
}

func TestRedactSensitive_Empty(t *testing.T) {
	output := RedactSensitive([]byte{})
	if len(output) != 0 {
		t.Errorf("expected empty output, got %d bytes", len(output))
	}
}

func TestContainsSensitive_True(t *testing.T) {
	if !ContainsSensitive([]byte("password = \"hunter2\"")) {
		t.Error("password should be detected as sensitive")
	}
	if !ContainsSensitive([]byte("sk-test12345678901234567")) {
		t.Error("API key should be detected as sensitive")
	}
}

func TestContainsSensitive_False(t *testing.T) {
	if ContainsSensitive([]byte("clean data")) {
		t.Error("clean data should not be detected as sensitive")
	}
	if ContainsSensitive([]byte{}) {
		t.Error("empty data should not be detected as sensitive")
	}
}

func TestStoreWithRedaction(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(filepath.Join(dir, "artifacts"))

	ref, redacted, err := s.StoreWithRedaction([]byte(`password = supersecret123`))
	if err != nil {
		t.Fatalf("StoreWithRedaction(): %v", err)
	}
	if !redacted {
		t.Error("expected redaction to occur")
	}
	if ref == "" {
		t.Error("ref should not be empty")
	}

	// Load and verify redacted content
	data, err := s.Load(ref)
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if bytes.Contains(data, []byte("supersecret123")) {
		t.Error("stored data should not contain original secret")
	}
	if !bytes.Contains(data, []byte("[REDACTED]")) {
		t.Error("stored data should contain redacted marker")
	}
}

func TestStoreWithRedaction_NoRedactNeeded(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(filepath.Join(dir, "artifacts"))

	ref, redacted, err := s.StoreWithRedaction([]byte("clean data"))
	if err != nil {
		t.Fatalf("StoreWithRedaction(): %v", err)
	}
	if redacted {
		t.Error("no redaction expected for clean data")
	}
	if ref == "" {
		t.Error("ref should not be empty")
	}
}

func TestStoreReader(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(filepath.Join(dir, "artifacts"))

	r := bytes.NewReader([]byte("reader test"))
	ref, err := s.StoreReader(r)
	if err != nil {
		t.Fatalf("StoreReader(): %v", err)
	}

	data, err := s.Load(ref)
	if err != nil {
		t.Fatalf("Load stored reader data: %v", err)
	}
	if string(data) != "reader test" {
		t.Errorf("got %q, want 'reader test'", data)
	}
}

func TestList_SortedByNewest(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(filepath.Join(dir, "artifacts"))

	s.Store([]byte("older"))
	s.Store([]byte("newer"))

	list, _ := s.List()
	if len(list) >= 2 && list[0].CreatedAt.Before(list[1].CreatedAt) {
		t.Error("List should return newest first")
	}
}

func TestNew_CreatesBaseDir(t *testing.T) {
	dir := t.TempDir()
	baseDir := filepath.Join(dir, "deep", "nested", "artifacts")
	s, err := New(baseDir)
	if err != nil {
		t.Fatalf("New() with nested dir: %v", err)
	}
	if _, err := os.Stat(baseDir); os.IsNotExist(err) {
		t.Fatal("base dir should be created")
	}
	_ = s // use s
}
