package storagenode

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const masterKeySetting = "storage.master_key"

// cipherBox encrypts and decrypts storage node credentials using AES-256-GCM.
type cipherBox struct {
	secretEnv string
	dataRoot  string
	db        *pgxpool.Pool

	mu  sync.Mutex
	key []byte
}

func newCipherBox(secretEnv, dataRoot string, db *pgxpool.Pool) *cipherBox {
	return &cipherBox{secretEnv: secretEnv, dataRoot: dataRoot, db: db}
}

func (c *cipherBox) resolveKey(ctx context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.key) == 32 {
		return c.key, nil
	}
	if c.secretEnv != "" {
		sum := sha256.Sum256([]byte("trainmaster-storage-v1:" + c.secretEnv))
		c.key = sum[:]
		return c.key, nil
	}
	if c.db != nil {
		var encoded string
		err := c.db.QueryRow(ctx, "SELECT value->>'key' FROM app_settings WHERE key = $1", masterKeySetting).Scan(&encoded)
		if err == nil && encoded != "" {
			raw, decErr := base64.StdEncoding.DecodeString(encoded)
			if decErr == nil && len(raw) == 32 {
				c.key = raw
				return c.key, nil
			}
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("load storage master key: %w", err)
		}
		gen := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, gen); err != nil {
			return nil, fmt.Errorf("generate storage master key: %w", err)
		}
		b64 := base64.StdEncoding.EncodeToString(gen)
		if err := c.db.QueryRow(ctx, `INSERT INTO app_settings (key, value, updated_at)
			VALUES ($1, jsonb_build_object('key', $2::text), now())
			ON CONFLICT (key) DO UPDATE SET updated_at = app_settings.updated_at
			RETURNING value->>'key'`, masterKeySetting, b64).Scan(&encoded); err != nil {
			return nil, fmt.Errorf("save storage master key: %w", err)
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(raw) != 32 {
			return nil, fmt.Errorf("invalid persisted storage master key")
		}
		c.key = raw
		return c.key, nil
	}

	// Fallback for DB-less unit tests: persist in dataRoot/.secret_key or memory.
	if c.dataRoot != "" {
		p := filepath.Join(c.dataRoot, ".secret_key")
		if data, err := os.ReadFile(p); err == nil {
			if raw, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data))); decErr == nil && len(raw) == 32 {
				c.key = raw
				return c.key, nil
			}
		}
		gen := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, gen); err != nil {
			return nil, err
		}
		_ = os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(gen)+"\n"), 0o600)
		c.key = gen
		return c.key, nil
	}

	gen := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, gen); err != nil {
		return nil, err
	}
	c.key = gen
	return c.key, nil
}

func (c *cipherBox) encrypt(ctx context.Context, creds Credentials) ([]byte, error) {
	if creds.SecretAccessKey == "" && creds.Password == "" {
		return nil, nil
	}
	key, err := c.resolveKey(ctx)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(creds)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 1+len(nonce)+len(plain)+gcm.Overhead())
	out[0] = 1 // version byte
	copy(out[1:], nonce)
	sealed := gcm.Seal(out[1+len(nonce):1+len(nonce)], nonce, plain, []byte("tm-storage-creds-v1"))
	return out[:1+len(nonce)+len(sealed)], nil
}

func (c *cipherBox) decrypt(ctx context.Context, enc []byte) (Credentials, error) {
	if len(enc) == 0 {
		return Credentials{}, nil
	}
	key, err := c.resolveKey(ctx)
	if err != nil {
		return Credentials{}, err
	}
	if enc[0] != 1 {
		return Credentials{}, errors.New("unsupported credential ciphertext version")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return Credentials{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Credentials{}, err
	}
	ns := gcm.NonceSize()
	if len(enc) < 1+ns+gcm.Overhead() {
		return Credentials{}, errors.New("corrupted credential ciphertext")
	}
	nonce := enc[1 : 1+ns]
	plain, err := gcm.Open(nil, nonce, enc[1+ns:], []byte("tm-storage-creds-v1"))
	if err != nil {
		return Credentials{}, fmt.Errorf("无法解密存储节点密钥（可能是 TM_SECRET_KEY 已变更）：%w", err)
	}
	var creds Credentials
	if err := json.Unmarshal(plain, &creds); err != nil {
		return Credentials{}, err
	}
	return creds, nil
}
