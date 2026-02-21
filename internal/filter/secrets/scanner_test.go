package secrets

import (
	"testing"

	"github.com/af-corp/aegis-gateway/internal/types"
)

func TestScanner_AWSKey(t *testing.T) {
	s := NewScanner()

	// True positive
	detections := s.Scan("my key is AKIAIOSFODNN7EXAMPLE")
	if len(detections) != 1 {
		t.Fatalf("expected 1 detection, got %d", len(detections))
	}
	if detections[0].PatternName != "AWS Access Key" {
		t.Errorf("expected AWS Access Key, got %s", detections[0].PatternName)
	}

	// False positive resistance: too short
	detections = s.Scan("AKIA1234")
	if len(detections) != 0 {
		t.Errorf("expected 0 detections for short AKIA, got %d", len(detections))
	}
}

func TestScanner_GCPServiceAccountKey(t *testing.T) {
	s := NewScanner()
	text := `{"private_key": "-----BEGIN PRIVATE KEY-----\nMIIE..."}`
	detections := s.Scan(text)

	found := false
	for _, d := range detections {
		if d.PatternName == "GCP Service Account Key" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected GCP Service Account Key detection")
	}
}

func TestScanner_GitHubToken(t *testing.T) {
	s := NewScanner()

	tokens := []string{
		"ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn",  // personal access token
		"gho_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn",  // OAuth
		"ghu_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn",  // user-to-server
		"ghs_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn",  // server-to-server
		"ghr_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmn",  // refresh token
	}

	for _, token := range tokens {
		detections := s.Scan("token: " + token)
		if len(detections) == 0 {
			t.Errorf("expected detection for GitHub token: %s", token[:10]+"...")
		}
	}

	// False positive: too short
	detections := s.Scan("ghp_short")
	if len(detections) != 0 {
		t.Errorf("expected 0 detections for short GitHub token, got %d", len(detections))
	}
}

func TestScanner_StripeKey(t *testing.T) {
	s := NewScanner()

	// Build strings at runtime to avoid GitHub push protection flagging the source file.
	liveKey := "sk_" + "live_" + "XXXXXXXXXXXXXXXXXXXXXXXX"
	detections := s.Scan(liveKey)
	if len(detections) == 0 {
		t.Error("expected Stripe key detection")
	}

	// Test key should not match
	testKey := "sk_" + "test_" + "XXXXXXXXXXXXXXXXXXXXXXXX"
	detections = s.Scan(testKey)
	if len(detections) != 0 {
		t.Errorf("expected 0 detections for sk_test, got %d", len(detections))
	}
}

func TestScanner_PrivateKey(t *testing.T) {
	s := NewScanner()

	keys := []string{
		"-----BEGIN PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----",
		"-----BEGIN DSA PRIVATE KEY-----",
	}

	for _, key := range keys {
		detections := s.Scan(key)
		if len(detections) == 0 {
			t.Errorf("expected detection for: %s", key)
		}
	}
}

func TestScanner_ConnectionString(t *testing.T) {
	s := NewScanner()

	connStrings := []string{
		"postgres://user:pass@host:5432/db",
		"mysql://root:secret@localhost/mydb",
		"mongodb://admin:password@mongo:27017",
		"redis://default:mypass@redis:6379",
	}

	for _, cs := range connStrings {
		detections := s.Scan("connect to " + cs)
		if len(detections) == 0 {
			t.Errorf("expected detection for connection string: %s", cs[:20]+"...")
		}
	}
}

func TestScanner_JWT(t *testing.T) {
	s := NewScanner()

	// Real JWT structure (header.payload.signature)
	jwt := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	detections := s.Scan("Bearer " + jwt)
	if len(detections) == 0 {
		t.Error("expected JWT detection")
	}
}

func TestScanner_CleanText(t *testing.T) {
	s := NewScanner()

	cleanTexts := []string{
		"Hello, how are you?",
		"Please help me write a function",
		"The API endpoint is /v1/chat/completions",
		"My email is user@example.com",
		"The password is hunter2",  // not a structured secret pattern
	}

	for _, text := range cleanTexts {
		detections := s.Scan(text)
		if len(detections) != 0 {
			t.Errorf("expected 0 detections for clean text %q, got %d", text, len(detections))
		}
	}
}

func TestScanner_MultipleSecrets(t *testing.T) {
	s := NewScanner()
	text := `Here is my AWS key: AKIAIOSFODNN7EXAMPLE and my db: postgres://user:pass@host/db`

	detections := s.Scan(text)
	if len(detections) < 2 {
		t.Errorf("expected at least 2 detections, got %d", len(detections))
	}

	names := map[string]bool{}
	for _, d := range detections {
		names[d.PatternName] = true
	}
	if !names["AWS Access Key"] {
		t.Error("expected AWS Access Key detection")
	}
	if !names["Connection String"] {
		t.Error("expected Connection String detection")
	}
}

func TestScanMessages(t *testing.T) {
	s := NewScanner()

	messages := []types.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Here is my key: AKIAIOSFODNN7EXAMPLE"},
	}

	detections := s.ScanMessages(messages)
	if len(detections) != 1 {
		t.Fatalf("expected 1 detection across messages, got %d", len(detections))
	}
	if detections[0].PatternName != "AWS Access Key" {
		t.Errorf("expected AWS Access Key, got %s", detections[0].PatternName)
	}
}

func TestScanMessages_Clean(t *testing.T) {
	s := NewScanner()

	messages := []types.Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "What is the capital of France?"},
	}

	detections := s.ScanMessages(messages)
	if len(detections) != 0 {
		t.Errorf("expected 0 detections for clean messages, got %d", len(detections))
	}
}

func BenchmarkScan_4KTokens(b *testing.B) {
	s := NewScanner()
	// ~4K tokens ≈ ~16KB of text
	text := ""
	for i := 0; i < 400; i++ {
		text += "This is a normal line of text that does not contain any secrets whatsoever. "
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Scan(text)
	}
}
