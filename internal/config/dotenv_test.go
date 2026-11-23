package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotEnv_BasicForms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env.test")
	content := "# comment\n" +
		"\n" +
		"PLAIN=value\n" +
		"QUOTED=\"hello world\"\n" +
		"SINGLE='raw value'\n" +
		"export EXPORTED=exported-value\n" +
		"WITH_EQUALS=postgres://u:p@h:5432/db?sslmode=disable\n" +
		"EMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// 用唯一的 key 前缀避免污染真实环境，测试后清理。
	keys := []string{"PLAIN", "QUOTED", "SINGLE", "EXPORTED", "WITH_EQUALS", "EMPTY"}
	for _, k := range keys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	defer func() {
		for _, k := range keys {
			os.Unsetenv(k)
		}
	}()

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}

	want := map[string]string{
		"PLAIN":       "value",
		"QUOTED":      "hello world",
		"SINGLE":      "raw value",
		"EXPORTED":    "exported-value",
		"WITH_EQUALS": "postgres://u:p@h:5432/db?sslmode=disable",
		"EMPTY":       "",
	}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestLoadDotEnv_DoesNotOverrideExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env.test")
	if err := os.WriteFile(path, []byte("DOTENV_PRESET=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DOTENV_PRESET", "from-env")
	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("DOTENV_PRESET"); got != "from-env" {
		t.Errorf("existing env should win, got %q", got)
	}
}

func TestLoadDotEnv_MissingFileReturnsNotExist(t *testing.T) {
	err := LoadDotEnv(filepath.Join(t.TempDir(), "nope.env"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected not-exist error, got %v", err)
	}
}

func TestLoadDotEnv_EscapesInDoubleQuotes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env.test")
	// Windows 路径里的反斜杠不是转义符，必须原样保留。
	content := "WINPATH=\"C:\\data\\warden\"\nNEWLINE=\"a\\nb\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv("WINPATH")
	os.Unsetenv("NEWLINE")
	defer func() {
		os.Unsetenv("WINPATH")
		os.Unsetenv("NEWLINE")
	}()

	if err := LoadDotEnv(path); err != nil {
		t.Fatalf("LoadDotEnv: %v", err)
	}
	if got := os.Getenv("WINPATH"); got != `C:\data\warden` {
		t.Errorf("WINPATH = %q, want C:\\data\\warden", got)
	}
	if got := os.Getenv("NEWLINE"); got != "a\nb" {
		t.Errorf("NEWLINE = %q, want a\\nb", got)
	}
}

func TestParseDotEnvLine_RejectsInvalid(t *testing.T) {
	cases := []string{"", "   ", "# comment", "no-equals-sign", "=no-key"}
	for _, c := range cases {
		if _, _, ok := parseDotEnvLine(c); ok {
			t.Errorf("line %q should be rejected", c)
		}
	}
}

// 真实场景：工作目录下有 .env.local 时，LoadFromOS 应能读到 DATABASE_URL。
func TestLoadFromOS_ReadsDotEnvLocal(t *testing.T) {
	dir := t.TempDir()
	content := "DATABASE_URL=\"postgres://localhost/datawarden\"\nJWT_SECRET=\"smoke-secret\"\n"
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// 切到临时目录，模拟从项目根启动。
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldwd)

	// 清掉可能存在的同名变量，确保读到的是文件内容。
	for _, k := range []string{"DATABASE_URL", "JWT_SECRET"} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			k := k
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}

	cfg, err := LoadFromOS()
	if err != nil {
		t.Fatalf("LoadFromOS: %v", err)
	}
	if cfg.DatabaseURL != "postgres://localhost/datawarden" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.JWTSecret != "smoke-secret" {
		t.Errorf("JWTSecret = %q", cfg.JWTSecret)
	}
}

// 逐级向上查找：配置在父目录时也能找到，适配 IDE 在子目录启动进程的情况。
func TestLoadFromDirChain_FindsParentDir(t *testing.T) {
	root := t.TempDir()
	content := "CHAIN_TEST_VAR=found-in-parent\n"
	if err := os.WriteFile(filepath.Join(root, ".env.local"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// 造一个深层子目录，模拟 cmd/server 这类嵌套结构。
	deep := filepath.Join(root, "cmd", "server")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	os.Unsetenv("CHAIN_TEST_VAR")
	t.Cleanup(func() { os.Unsetenv("CHAIN_TEST_VAR") })

	loaded, err := loadFromDirChain(deep)
	if err != nil {
		t.Fatalf("loadFromDirChain: %v", err)
	}
	if !loaded {
		t.Fatal("expected to find .env.local in parent directory")
	}
	if got := os.Getenv("CHAIN_TEST_VAR"); got != "found-in-parent" {
		t.Errorf("CHAIN_TEST_VAR = %q, want found-in-parent", got)
	}
}

// 显式指定文件时，文件不存在应当报错，避免静默忽略配置错误。
func TestLoadFromOS_ExplicitFileMissingErrors(t *testing.T) {
	t.Setenv(envFileOverride, filepath.Join(t.TempDir(), "absent.env"))
	if _, err := LoadFromOS(); err == nil {
		t.Fatal("expected error when explicit env file is missing")
	}
}

// 显式指定的文件应当被读取。
func TestLoadFromOS_ExplicitFileOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "custom.env")
	content := "DATABASE_URL=\"postgres://explicit/db\"\nJWT_SECRET=\"explicit-secret\"\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, k := range []string{"DATABASE_URL", "JWT_SECRET"} {
		if v, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			k := k
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	t.Setenv(envFileOverride, p)

	cfg, err := LoadFromOS()
	if err != nil {
		t.Fatalf("LoadFromOS: %v", err)
	}
	if cfg.DatabaseURL != "postgres://explicit/db" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
}
