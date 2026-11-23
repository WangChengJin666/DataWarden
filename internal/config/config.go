package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// Config 是全应用唯一的配置来源。所有模块只接受 Config，不直接读环境变量。
type Config struct {
	HTTPAddr         string
	DatabaseURL      string
	JWTSecret        string
	QueryTimeoutSec  int
	RedisScanMaxKeys int
	QueryRowLimit    int
	// AuthzRefreshSec 是 Casbin 策略的重建间隔（秒）。
	//
	// 授权记录带 expires_at，但 Casbin 的策略在内存里：
	// 只在启动和「审批通过」两个时刻重建的话，一条过期授权会一直生效到下次重启，
	// 这是真实的越权窗口。所以必须定时把数据库现状刷回内存。
	AuthzRefreshSec int
}

// Load 从 getenv 读取配置。getenv 可注入，便于测试。
func Load(getenv func(string) string) (*Config, error) {
	cfg := &Config{
		HTTPAddr:         orDefault(getenv("HTTP_ADDR"), ":8080"),
		DatabaseURL:      getenv("DATABASE_URL"),
		JWTSecret:        getenv("JWT_SECRET"),
		QueryTimeoutSec:  orDefaultInt(getenv("QUERY_TIMEOUT_SEC"), 30),
		RedisScanMaxKeys: orDefaultInt(getenv("REDIS_SCAN_MAX_KEYS"), 200),
		QueryRowLimit:    orDefaultInt(getenv("QUERY_ROW_LIMIT"), 500),
		AuthzRefreshSec:  orDefaultInt(getenv("AUTHZ_REFRESH_SEC"), 60),
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	return cfg, nil
}

// LoadFromOS 是 Load 的生产入口。
//
// 启动时会尝试加载 .env.local / .env 文件，这样在 LiteIDE、VS Code 等
// 直接运行二进制的场景下也能拿到配置，不必再手动 `source .env.local`。
//
// 查找顺序（找到第一个就停）：
//  1. 环境变量 DATAWARDEN_ENV_FILE 指定的路径（最高优先级，便于特殊部署）
//  2. 当前工作目录及其各级父目录
//  3. 可执行文件所在目录及其各级父目录
//
// 第 3 步是为 IDE 场景准备的：LiteIDE 运行 build 出来的二进制时，
// 工作目录常常是 IDE 自身目录而非项目根，靠可执行文件位置才能定位到项目。
//
// 文件缺失不算错误（可能配置来自真实的进程环境或服务管理器）；
// 只有文件存在却读取失败（权限、编码等）才向上报告。
func LoadFromOS() (*Config, error) {
	if p := os.Getenv(envFileOverride); p != "" {
		if err := loadSingleEnvFile(p); err != nil {
			return nil, fmt.Errorf("load %s: %w", p, err)
		}
		return Load(os.Getenv)
	}

	roots := searchRoots()
	if err := loadFirstEnvFile(roots); err != nil {
		return nil, err
	}
	return Load(os.Getenv)
}

const envFileOverride = "DATAWARDEN_ENV_FILE"

// dotEnvCandidates 按优先级排列，先出现的先加载（先到先得，不覆盖已有值）。
var dotEnvCandidates = []string{".env.local", ".env"}

// searchRoots 返回按优先级排列的查找起始目录。
func searchRoots() []string {
	var roots []string
	if wd, err := os.Getwd(); err == nil {
		roots = append(roots, wd)
	}
	if exe, err := os.Executable(); err == nil {
		// 解析符号链接，得到真实路径。
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		roots = append(roots, filepath.Dir(exe))
	}
	return roots
}

// loadFirstEnvFile 依次尝试各起始目录，在找到的第一个配置文件处停止。
func loadFirstEnvFile(roots []string) error {
	for _, root := range roots {
		loaded, err := loadFromDirChain(root)
		if err != nil {
			return err
		}
		if loaded {
			return nil
		}
	}
	return nil
}

// loadFromDirChain 从 dir 开始逐级向上查找配置文件。
// 返回是否找到并加载了文件。
func loadFromDirChain(dir string) (bool, error) {
	for {
		for _, name := range dotEnvCandidates {
			p := filepath.Join(dir, name)
			err := LoadDotEnv(p)
			if err == nil {
				return true, nil
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return false, fmt.Errorf("load %s: %w", p, err)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}
		dir = parent
	}
}

// loadSingleEnvFile 加载显式指定的文件，文件不存在时报错而非静默忽略。
func loadSingleEnvFile(path string) error {
	if err := LoadDotEnv(path); err != nil {
		return err
	}
	return nil
}


func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orDefaultInt(v string, def int) int {
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
