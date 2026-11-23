package config

import (
	"bufio"
	"os"
	"strings"
)

// LoadDotEnv 读取 .env 风格的键值文件，把其中的变量写入进程环境。
//
// 设计要点：
//   - 已存在于进程环境中的变量不会被覆盖，保证显式 export 的值优先级更高；
//   - 文件不存在时返回 os.ErrNotExist，调用方应把它视为"未配置"而非错误；
//   - 只支持实际用到的语法：空行、# 注释、KEY=VALUE、可选的双引号/单引号包裹。
//
// 之所以不引入第三方 dotenv 库：本项目的 .env 格式极简，自带实现可少一个依赖，
// 也避免为 IDE/GUI 启动场景额外增加供应链面积。
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// 单行可能很长（例如带证书的 URL），放宽到 1MB。
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		key, val, ok := parseDotEnvLine(scanner.Text())
		if !ok {
			continue
		}
		// 进程环境已有的值优先，不覆盖。
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// parseDotEnvLine 解析单行，返回 key、value 以及该行是否为有效赋值。
func parseDotEnvLine(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}

	// 允许 "export KEY=VALUE" 写法。
	if rest, found := strings.CutPrefix(line, "export "); found {
		line = strings.TrimSpace(rest)
	}

	idx := strings.Index(line, "=")
	if idx <= 0 {
		return "", "", false
	}

	key = strings.TrimSpace(line[:idx])
	if key == "" {
		return "", "", false
	}

	val = strings.TrimSpace(line[idx+1:])

	// 去掉成对的引号；未成对时按普通值处理，避免误伤含引号的内容。
	if len(val) >= 2 {
		if (val[0] == '"' && val[len(val)-1] == '"') ||
			(val[0] == '\'' && val[len(val)-1] == '\'') {
			quote := val[0]
			val = val[1 : len(val)-1]
			// 双引号内的 \n \t \" 做转义还原，单引号内保持字面量。
			if quote == '"' {
				val = unescapeDoubleQuoted(val)
			}
		}
	}
	return key, val, true
}

// unescapeDoubleQuoted 还原双引号内的常见转义序列。
func unescapeDoubleQuoted(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i == len(s)-1 {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		default:
			// 未知转义原样保留，例如 Windows 路径 C:\data。
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
