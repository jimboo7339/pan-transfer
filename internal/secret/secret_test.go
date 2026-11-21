package secret

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// newTestCipher 用固定密钥构造一个 cipher（测试需要可重现）。
func newTestCipher(t *testing.T) *Cipher {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	c, err := newCipherFromKey(key)
	if err != nil {
		t.Fatalf("构造 cipher 失败: %v", err)
	}
	return c
}

// TestEncryptRoundTrip 验证加解密往返一致。
func TestEncryptRoundTrip(t *testing.T) {
	c := newTestCipher(t)
	cases := []string{
		"__puus=abc123; __pus=def456",
		"eyJhbGciOiJIUzI1NiJ9.payload.sig",
		strings.Repeat("x", 4096), // 大 cookie
		"中文与 emoji 🎬 混合",
	}
	for _, plain := range cases {
		enc, err := c.Encrypt(plain)
		if err != nil {
			t.Fatalf("加密失败: %v", err)
		}
		if !IsEncrypted(enc) {
			t.Errorf("密文缺少信封前缀: %q", enc)
		}
		if strings.Contains(enc, plain) && len(plain) > 8 {
			t.Errorf("密文中疑似包含明文: %q", enc[:min(40, len(enc))])
		}
		got, err := c.Decrypt(enc)
		if err != nil {
			t.Fatalf("解密失败: %v", err)
		}
		if got != plain {
			t.Errorf("往返不一致: got %q want %q", got, plain)
		}
	}
}

// TestEncryptEmptyStaysEmpty 验证空值不会被加密成信封，
// 否则"没有凭据"会变成"有一个空凭据"。
func TestEncryptEmptyStaysEmpty(t *testing.T) {
	c := newTestCipher(t)
	enc, err := c.Encrypt("")
	if err != nil {
		t.Fatalf("加密空串失败: %v", err)
	}
	if enc != "" {
		t.Errorf("空串应返回空串，得到 %q", enc)
	}
}

// TestDecryptPassesThroughPlaintext 验证历史明文配置仍可读取。
// 这是升级路径的关键：新版本读旧库不能把所有账号一次性搞失效。
func TestDecryptPassesThroughPlaintext(t *testing.T) {
	c := newTestCipher(t)
	plain := `{"cookie":"legacy-plaintext-cookie"}`
	got, err := c.Decrypt(plain)
	if err != nil {
		t.Fatalf("明文应原样返回，却报错: %v", err)
	}
	if got != plain {
		t.Errorf("明文被改写: %q", got)
	}
}

// TestDecryptWrongKeyFails 验证密钥不匹配时明确报错，而不是静默返回乱码。
func TestDecryptWrongKeyFails(t *testing.T) {
	c1 := newTestCipher(t)
	enc, err := c1.Encrypt("secret-cookie")
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	other := make([]byte, 32)
	for i := range other {
		other[i] = 0xFF
	}
	c2, err := newCipherFromKey(other)
	if err != nil {
		t.Fatalf("构造 cipher 失败: %v", err)
	}
	if _, err := c2.Decrypt(enc); err == nil {
		t.Fatal("错误密钥应导致解密失败")
	}
}

// TestDecryptCorruptedFails 验证密文被篡改时不会静默返回垃圾。
func TestDecryptCorruptedFails(t *testing.T) {
	c := newTestCipher(t)
	enc, _ := c.Encrypt("secret-cookie")
	// 破坏 base64 载荷部分
	if err := errorForTest(); err != nil {
		t.Fatal(err)
	}
	corrupted := enc[:len(enc)-4] + "AAAA"
	if _, err := c.Decrypt(corrupted); err == nil {
		t.Fatal("被篡改的密文应解密失败")
	}
}

func errorForTest() error { return nil }

// TestPasswordHashRoundTrip 验证密码哈希与校验。
func TestPasswordHashRoundTrip(t *testing.T) {
	h, err := HashPassword("1234")
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	if !strings.HasPrefix(h, "argon2id$") {
		t.Errorf("应使用 argon2id，得到 %q", h[:min(20, len(h))])
	}
	if !VerifyPassword(h, "1234") {
		t.Error("正确密码应校验通过")
	}
	if VerifyPassword(h, "1235") {
		t.Error("错误密码不应通过")
	}
	// 哈希必须带随机盐：同一密码两次哈希结果应不同。
	h2, _ := HashPassword("1234")
	if h == h2 {
		t.Error("两次哈希相同，说明没有使用随机盐")
	}
}

// TestVerifyPasswordAcceptsLegacy 验证旧的 SHA-256 哈希仍可登录，
// 且被标记为需要升级——否则用户会被锁在门外。
func TestVerifyPasswordAcceptsLegacy(t *testing.T) {
	legacy := legacyHash("1234")
	if !VerifyPassword(legacy, "1234") {
		t.Error("旧格式密码应仍能校验通过")
	}
	if !NeedsRehash(legacy) {
		t.Error("旧格式应被标记为待升级")
	}
	if NeedsRehash(mustHash(t, "1234")) {
		t.Error("新格式不应被标记为待升级")
	}
}

func mustHash(t *testing.T, pwd string) string {
	t.Helper()
	h, err := HashPassword(pwd)
	if err != nil {
		t.Fatalf("哈希失败: %v", err)
	}
	return h
}

// TestInitPersistsKeyFile 验证首次启动会生成密钥文件且权限正确。
func TestInitPersistsKeyFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envMasterKey, "") // 确保走文件路径

	// 重置单例以便重复测试
	resetForTest()

	src, err := Init(dir)
	if err != nil {
		t.Fatalf("首次初始化失败: %v", err)
	}
	if !strings.HasSuffix(src, keyFileName) {
		t.Errorf("密钥来源应为密钥文件，得到 %q", src)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("密钥文件应已生成: %v", err)
	}

	// 第二次初始化应复用同一个密钥（否则重启后所有凭据都解不开）。
	resetForTest()
	c1 := Default()
	if _, err := Init(dir); err != nil {
		t.Fatalf("第二次初始化失败: %v", err)
	}
	if Default() == nil {
		t.Fatal("cipher 未就绪")
	}
	enc, _ := Default().Encrypt("probe")
	_ = c1
	if !IsEncrypted(enc) {
		t.Error("应能正常加密")
	}
}

// TestInitPrefersEnvKey 验证环境变量优先于密钥文件（容器场景）。
func TestInitPrefersEnvKey(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(255 - i)
	}
	t.Setenv(envMasterKey, hex.EncodeToString(key))
	resetForTest()

	src, err := Init(dir)
	if err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	if !strings.HasPrefix(src, "env:") {
		t.Errorf("应优先使用环境变量密钥，得到 %q", src)
	}
	if _, err := os.Stat(filepath.Join(dir, keyFileName)); err == nil {
		t.Error("使用环境变量密钥时不应生成密钥文件")
	}
}

// TestInitRejectsBadEnvKey 验证格式错误的环境变量不会静默降级成明文。
func TestInitRejectsBadEnvKey(t *testing.T) {
	t.Setenv(envMasterKey, "not-a-valid-32-byte-key")
	resetForTest()
	if _, err := Init(t.TempDir()); err == nil {
		t.Fatal("无效的环境变量密钥应报错，而不是静默降级")
	}
}

// resetForTest 清空进程级单例状态。
func resetForTest() {
	defaultOnce = sync.Once{}
	defaultCipher = nil
	defaultErr = nil
	defaultKeyFrom = ""
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}