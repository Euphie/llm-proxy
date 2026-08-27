package agenttrajectory

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactMasksSensitiveKeysAndPlaintextSecrets(t *testing.T) {
	events := []Event{
		{
			Role: "assistant", Kind: EventToolCall, ToolName: "deploy",
			Arguments: json.RawMessage(`{
				"password":"plain-password",
				"nested":{"api_key":"sk-secret-value","safe":"visible"},
				"items":[{"authorization":"Bearer nested-secret"}],
				"note":"Authorization: Bearer text-secret"
			}`),
		},
		{
			Role: "tool", Kind: EventToolResult,
			Result: json.RawMessage(`{"cookie":"session=raw-cookie","message":"token=plain-token; safe=yes"}`),
		},
		{Role: "assistant", Kind: EventAssistantText, Text: "use Bearer final-secret and sk-live-1234567890"},
	}

	redacted := Redact(events)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	got := string(encoded)
	for _, secret := range []string{
		"plain-password", "sk-secret-value", "nested-secret", "text-secret",
		"raw-cookie", "plain-token", "final-secret", "sk-live-1234567890",
	} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted payload contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, `"safe":"visible"`) || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("redacted payload=%s", got)
	}
	if !bytes.Contains(events[0].Arguments, []byte("plain-password")) {
		t.Fatal("Redact mutated the caller's events")
	}
}

func TestOpenCipherCreatesAndReusesPrivateKey(t *testing.T) {
	dataDir := t.TempDir()
	first, err := OpenCipher(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(dataDir, keyFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() || info.Size() != 32 {
		t.Fatalf("key mode=%v size=%d", info.Mode(), info.Size())
	}

	payload, err := first.Seal(Plaintext{Events: []Event{{Role: "user", Kind: EventUserMessage, Text: "hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenCipher(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := second.Open(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Events) != 1 || opened.Events[0].Text != "hello" {
		t.Fatalf("opened=%+v", opened)
	}
}

func TestCipherRoundTripUsesRandomNonceAndRejectsTampering(t *testing.T) {
	cipher, err := OpenCipher(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plain := Plaintext{
		Events:    []Event{{Role: "assistant", Kind: EventAssistantText, Text: "result"}},
		FinalText: "result",
	}
	first, err := cipher.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	second, err := cipher.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != encryptionVersion || bytes.Equal(first.Nonce, second.Nonce) || bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	opened, err := cipher.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	if opened.FinalText != plain.FinalText || len(opened.Events) != 1 || opened.Events[0].Text != "result" {
		t.Fatalf("opened=%+v", opened)
	}

	tampered := first
	tampered.Ciphertext = append([]byte(nil), first.Ciphertext...)
	tampered.Ciphertext[0] ^= 0xff
	if _, err := cipher.Open(tampered); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tamper err=%v", err)
	}
	tampered = first
	tampered.Version++
	if _, err := cipher.Open(tampered); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("version err=%v", err)
	}
}

func TestCipherRedactsBeforeEncryptionAndDoesNotLeakPlaintext(t *testing.T) {
	dataDir := t.TempDir()
	cipher, err := OpenCipher(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	secret := "never-persist-this-secret"
	payload, err := cipher.Seal(Plaintext{Events: []Event{{
		Role: "assistant", Kind: EventToolCall,
		Arguments: json.RawMessage(`{"password":"` + secret + `"}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatalf("encrypted payload leaks %q", secret)
	}
	opened, err := cipher.Open(payload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(opened.Events[0].Arguments, []byte(secret)) || !bytes.Contains(opened.Events[0].Arguments, []byte("[REDACTED]")) {
		t.Fatalf("opened arguments=%s", opened.Events[0].Arguments)
	}
}

func TestOpenCipherRejectsUnsafeExistingKeyFiles(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			name: "short key",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, bytes.Repeat([]byte{1}, 31), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "permissive mode",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, bytes.Repeat([]byte{1}, 32), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				target := filepath.Join(filepath.Dir(path), "target")
				if err := os.WriteFile(target, bytes.Repeat([]byte{1}, 32), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataDir := t.TempDir()
			tt.setup(t, filepath.Join(dataDir, keyFilename))
			if _, err := OpenCipher(dataDir); !errors.Is(err, ErrInvalidKeyFile) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
