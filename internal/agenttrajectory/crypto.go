package agenttrajectory

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	keyFilename       = "agent-trajectory.key"
	encryptionVersion = 1
	keySize           = 32
)

var (
	ErrInvalidKeyFile = errors.New("invalid agent trajectory key file")
	ErrDecrypt        = errors.New("decrypt agent trajectory")
)

type Plaintext struct {
	Events                  []Event          `json:"events"`
	FinalText               string           `json:"final_text,omitempty"`
	FinalOutputCostMicroUSD int64            `json:"final_output_cost_micro_usd,omitempty"`
	FinalOutputLatencyMS    int64            `json:"final_output_latency_ms,omitempty"`
	FinalOutputMetricsKnown bool             `json:"final_output_metrics_known,omitempty"`
	ModelPath               []string         `json:"model_path,omitempty"`
	SelfEscalations         []SelfEscalation `json:"self_escalations,omitempty"`
}

type SelfEscalation struct {
	FromModel string `json:"from_model"`
	ToModel   string `json:"to_model"`
	Reason    string `json:"reason"`
}

type EncryptedPayload struct {
	Version    int    `json:"version"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

type Cipher struct {
	aead cipher.AEAD
}

func OpenCipher(dataDir string) (*Cipher, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, ErrInvalidKeyFile
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create agent trajectory key directory: %w", err)
	}
	path := filepath.Join(dataDir, keyFilename)
	key, err := loadOrCreateKey(path)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create agent trajectory cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create agent trajectory AEAD: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

func (c *Cipher) Seal(plaintext Plaintext) (EncryptedPayload, error) {
	if c == nil || c.aead == nil {
		return EncryptedPayload{}, ErrInvalidKeyFile
	}
	plaintext.Events = Redact(plaintext.Events)
	plaintext.FinalText = redactText(plaintext.FinalText)
	encoded, err := json.Marshal(plaintext)
	if err != nil {
		return EncryptedPayload{}, fmt.Errorf("encode agent trajectory: %w", err)
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return EncryptedPayload{}, fmt.Errorf("generate agent trajectory nonce: %w", err)
	}
	return EncryptedPayload{
		Version:    encryptionVersion,
		Nonce:      nonce,
		Ciphertext: c.aead.Seal(nil, nonce, encoded, encryptionAAD(encryptionVersion)),
	}, nil
}

func (c *Cipher) Open(payload EncryptedPayload) (Plaintext, error) {
	if c == nil || c.aead == nil || payload.Version != encryptionVersion ||
		len(payload.Nonce) != c.aead.NonceSize() || len(payload.Ciphertext) < c.aead.Overhead() {
		return Plaintext{}, ErrDecrypt
	}
	encoded, err := c.aead.Open(nil, payload.Nonce, payload.Ciphertext, encryptionAAD(payload.Version))
	if err != nil {
		return Plaintext{}, fmt.Errorf("%w: %v", ErrDecrypt, err)
	}
	var plaintext Plaintext
	if json.Unmarshal(encoded, &plaintext) != nil {
		return Plaintext{}, ErrDecrypt
	}
	return plaintext, nil
}

func loadOrCreateKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err == nil {
		return readKey(path, info)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect agent trajectory key: %w", err)
	}
	if err := createKeyAtomically(path); err != nil {
		return nil, err
	}
	info, err = os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect generated agent trajectory key: %w", err)
	}
	return readKey(path, info)
}

func readKey(path string, info os.FileInfo) ([]byte, error) {
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != keySize {
		return nil, ErrInvalidKeyFile
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read agent trajectory key: %w", err)
	}
	if len(key) != keySize {
		return nil, ErrInvalidKeyFile
	}
	return key, nil
}

func createKeyAtomically(path string) error {
	key := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return fmt.Errorf("generate agent trajectory key: %w", err)
	}
	suffix := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, suffix); err != nil {
		return fmt.Errorf("generate agent trajectory key filename: %w", err)
	}
	temporaryPath := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+"."+hex.EncodeToString(suffix)+".tmp")
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create temporary agent trajectory key: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := file.Write(key); err != nil {
		_ = file.Close()
		return fmt.Errorf("write agent trajectory key: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync agent trajectory key: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close agent trajectory key: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install agent trajectory key: %w", err)
	}
	removeTemporary = false
	return nil
}

func encryptionAAD(version int) []byte {
	return []byte(fmt.Sprintf("llm-proxy/agent-trajectory/v%d", version))
}
