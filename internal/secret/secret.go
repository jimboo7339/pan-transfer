// Package secret provides the at-rest protection PanTransfer needs for the
// credentials users entrust to it: netdisk cookies and OAuth tokens.
//
// Design constraints (why it looks like this):
//
//  1. The database file is the only durable state a user has. If it leaks, every
//     netdisk account it describes is compromised — and Quark/Guangya are
//     reverse-engineered Web APIs, so a leaked cookie is a fully usable session.
//     Therefore account configs are encrypted before they reach SQLite.
//
//  2. The key must not live next to the ciphertext, or encryption buys nothing.
//     Priority: PAN_TRANSFER_MASTER_KEY env var → data/master.key file (0600).
//
//  3. Old databases contain plaintext configs. Those must keep working after
//     upgrade, otherwise the first start of a new version bricks every account.
//     Detection is by envelope prefix, so plaintext passes through untouched.
//
//  4. A failed decrypt must never take the whole service down. The caller needs
//     to know *which* accounts are unreadable so it can report them.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// envMasterKey is the preferred key source for container deployments.
const envMasterKey = "PAN_TRANSFER_MASTER_KEY"

// keyFileName is the fallback key file, kept beside the SQLite database.
const keyFileName = "master.key"

// prefix marks a ciphertext produced by this package. Plaintext configs (from
// databases created before encryption existed) never carry it.
const prefix = "enc:v1:"

// ErrNotEncrypted reports that a value is not a ciphertext envelope.
var ErrNotEncrypted = errors.New("secret: value is not encrypted")

// ErrDecryptFailed reports a ciphertext that could not be authenticated.
var ErrDecryptFailed = errors.New("secret: decryption failed (wrong key or corrupted data)")

// Cipher encrypts and decrypts credential payloads.
//
// A Cipher is safe for concurrent use: the underlying AES-GCM AEAD is
// stateless, and the key is read once at construction.
type Cipher struct {
	aead cipher.AEAD
}

var (
	defaultOnce    sync.Once
	defaultCipher  *Cipher
	defaultErr     error
	defaultKeyFrom string
)

// Init loads the master key (env var first, then data/master.key) and prepares
// the process-wide cipher. It is called once during startup, before the store
// opens. Safe to call multiple times; only the first call has an effect.
//
// Returns the key source ("env" or the key file path) for the startup log, so
// operators can see where protection comes from.
func Init(dataDir string) (string, error) {
	defaultOnce.Do(func() {
		defaultCipher, defaultKeyFrom, defaultErr = newCipher(dataDir)
	})
	return defaultKeyFrom, defaultErr
}

// Default returns the process-wide cipher. Returns nil before Init.
func Default() *Cipher { return defaultCipher }

// newCipher resolves the master key and builds a cipher.
func newCipher(dataDir string) (*Cipher, string, error) {
	if v := strings.TrimSpace(os.Getenv(envMasterKey)); v != "" {
		key, err := decodeKey(v)
		if err != nil {
			return nil, "", fmt.Errorf("secret: %s 格式无效: %w", envMasterKey, err)
		}
		c, err := newCipherFromKey(key)
		if err != nil {
			return nil, "", err
		}
		return c, "env:" + envMasterKey, nil
	}

	if dataDir == "" {
		return nil, "", errors.New("secret: 数据目录为空，无法确定密钥文件位置")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, "", fmt.Errorf("secret: 创建数据目录失败: %w", err)
	}
	path := filepath.Join(dataDir, keyFileName)

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, derr := decodeKey(strings.TrimSpace(string(raw)))
		if derr != nil {
			return nil, "", fmt.Errorf("secret: 密钥文件 %s 内容无效: %w", path, derr)
		}
		c, cerr := newCipherFromKey(key)
		if cerr != nil {
			return nil, "", cerr
		}
		return c, path, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, "", fmt.Errorf("secret: 读取密钥文件失败: %w", err)
	}

	// First run: generate a fresh key and persist it with owner-only permissions.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, "", fmt.Errorf("secret: 生成主密钥失败: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(key)), 0o600); err != nil {
		return nil, "", fmt.Errorf("secret: 写入密钥文件失败: %w", err)
	}
	c, err := newCipherFromKey(key)
	if err != nil {
		return nil, "", err
	}
	return c, path, nil
}

// decodeKey accepts either 64 hex chars or a base64-encoded 32-byte key.
func decodeKey(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if len(s) == 64 {
		if b, err := hex.DecodeString(s); err == nil {
			return b, nil
		}
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, errors.New("需要 32 字节密钥（64 位十六进制或 base64）")
}

func newCipherFromKey(key []byte) (*Cipher, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret: 密钥长度应为 32 字节，实际 %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret: 初始化 AES 失败: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: 初始化 GCM 失败: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Encrypt returns an envelope: prefix + base64(nonce || ciphertext || tag).
// Empty input returns empty, so absent credentials stay absent.
func (c *Cipher) Encrypt(plaintext string) (string, error) {
	if c == nil {
		return "", errors.New("secret: cipher 未初始化")
	}
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("secret: 生成 nonce 失败: %w", err)
	}
	sealed := c.aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return prefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt reverses Encrypt. Values without the envelope prefix are returned
// unchanged, which is what makes pre-encryption databases keep working.
func (c *Cipher) Decrypt(value string) (string, error) {
	if value == "" || !strings.HasPrefix(value, prefix) {
		return value, nil
	}
	if c == nil {
		return "", errors.New("secret: cipher 未初始化")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, prefix))
	if err != nil {
		return "", fmt.Errorf("%w: 编码无效", ErrDecryptFailed)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", fmt.Errorf("%w: 数据过短", ErrDecryptFailed)
	}
	nonce, sealed := raw[:ns], raw[ns:]
	plain, err := c.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return "", fmt.Errorf("%w", ErrDecryptFailed)
	}
	return string(plain), nil
}

// IsEncrypted reports whether a stored value is a ciphertext envelope.
func IsEncrypted(value string) bool { return strings.HasPrefix(value, prefix) }

// ---------- password hashing ----------

// argon2 parameters. 64 MiB / 3 passes / 4 lanes follows the RFC 9106
// recommendation's second recommended configuration (the first, 2 GiB, is
// overkill for a single-user self-hosted login).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 4
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword derives an Argon2id hash encoded as
// "argon2id$v=19$m=..,t=..,p=..$<salt-b64>$<hash-b64>".
//
// The previous implementation was a single unstretched SHA-256, which made a
// leaked database trivially brute-forceable for the short numeric passwords
// people actually choose for a home NAS panel.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("secret: 生成盐失败: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against a stored hash.
//
// Hashes produced by the legacy SHA-256 scheme are still accepted and silently
// upgraded by the caller, so enabling encryption does not lock users out.
func VerifyPassword(encoded, password string) bool {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return false
	}
	if legacyHashRe.MatchString(encoded) {
		return subtleEqualString(encoded, legacyHash(password))
	}
	parts := strings.Split(encoded, "$")
	// 格式：argon2id$v=19$m=..,t=..,p=..$<salt>$<hash>
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	var version int
	var memory uint32
	var time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[1], "v=%d", &version); err != nil {
		return false
	}
	if version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	return subtleEqualBytes(got, want)
}

// NeedsRehash reports whether a stored hash uses the legacy scheme and should
// be replaced with an Argon2id hash after a successful login.
func NeedsRehash(encoded string) bool {
	return legacyHashRe.MatchString(strings.TrimSpace(encoded))
}

// hashPasswordPrefix is the domain separator used by the legacy scheme.
const hashPasswordPrefix = "pan-transfer:"

// legacyHash reproduces the pre-encryption password hash so existing installs
// can still log in and be upgraded in place.
func legacyHash(password string) string {
	sum := sha256.Sum256([]byte(hashPasswordPrefix + password))
	return hex.EncodeToString(sum[:])
}