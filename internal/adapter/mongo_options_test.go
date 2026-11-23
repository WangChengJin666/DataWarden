package adapter

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// newClientOptionsForTest 给参数校验用例提供一个干净的客户端选项对象。
func newClientOptionsForTest() *options.ClientOptions {
	return options.Client()
}

// 副本集只能填一个 host 时，必须靠 replicaSet 参数让驱动去发现其他成员；
// 多节点写法则要正确拆成 seed list。这两种形态都直接影响「能不能连上」。
func TestParseMongoHosts(t *testing.T) {
	cases := []struct {
		host string
		port int
		want []string
	}{
		{"10.0.0.1", 27017, []string{"10.0.0.1:27017"}},
		{"10.0.0.1,10.0.0.2", 27017, []string{"10.0.0.1:27017", "10.0.0.2:27017"}},
		{" 10.0.0.1 , 10.0.0.2 ", 27018, []string{"10.0.0.1:27018", "10.0.0.2:27018"}},
		{"10.0.0.1:27019", 27017, []string{"10.0.0.1:27019"}},
		{"[::1]", 27017, []string{"[::1]:27017"}},
		{"[::1]:27018", 27017, []string{"[::1]:27018"}},
	}

	for _, c := range cases {
		got, err := parseMongoHosts(c.host, c.port)
		if err != nil {
			t.Errorf("parseMongoHosts(%q) error: %v", c.host, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("parseMongoHosts(%q) = %v, want %v", c.host, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseMongoHosts(%q)[%d] = %q, want %q", c.host, i, got[i], c.want[i])
			}
		}
	}
}

// 空 host 必须报错而不是拼出 ":27017"（驱动会把它当成 localhost，
// 从而静默连到错误的目标）。
func TestParseMongoHosts_Empty(t *testing.T) {
	if _, err := parseMongoHosts("", 27017); err == nil {
		t.Fatal("empty host must be rejected")
	}
	if _, err := parseMongoHosts("  ,  ", 27017); err == nil {
		t.Fatal("host list with only blanks must be rejected")
	}
}

// IPv6 裸写（未加方括号且带端口）无法区分「地址」和「端口」，必须明确报错。
func TestParseMongoHosts_IPv6WithoutBrackets(t *testing.T) {
	_, err := parseMongoHosts("::1:27017", 27017)
	if err == nil {
		t.Fatal("bare IPv6 with port must be rejected")
	}
	if !strings.Contains(err.Error(), "方括号") {
		t.Errorf("error should hint at brackets, got: %v", err)
	}
}

// 白名单之外的参数必须报错：静默忽略会让人以为配置生效了。
func TestNewMongo_RejectsUnknownOption(t *testing.T) {
	_, err := newMongo(ConnConfig{
		Type:     "mongodb",
		Host:     "127.0.0.1",
		Port:     27017,
		Database: "test",
		Options:  map[string]string{"connectTimeoutMS": "1000"},
	})
	if err == nil {
		t.Fatal("unknown option must be rejected")
	}
	if !strings.Contains(err.Error(), "connectTimeoutMS") {
		t.Errorf("error should name the option, got: %v", err)
	}
}

// 参数值校验失败要报出参数名，不然用户不知道是哪一项填错了。
func TestApplyMongoOption_InvalidValues(t *testing.T) {
	cases := []struct {
		key, val string
		wantHear string
	}{
		{"tls", "yes", "tls"},
		{"directConnection", "maybe", "directConnection"},
		{"readPreference", "fastest", "readPreference"},
		{"tlsCAFile", "/nonexistent/ca.pem", "CA"},
	}
	for _, c := range cases {
		opts := newClientOptionsForTest()
		err := applyMongoOption(opts, c.key, c.val)
		if err == nil {
			t.Errorf("%s=%q should be rejected", c.key, c.val)
			continue
		}
		if !strings.Contains(err.Error(), c.wantHear) {
			t.Errorf("%s=%q error should mention %q, got: %v", c.key, c.val, c.wantHear, err)
		}
	}
}

// --- TLS 参数必须聚合，不能互相覆盖 ---
//
// 回归测试：早先每个 TLS 参数各自调 SetTLSConfig（整体替换），
// 而 map 遍历顺序随机。tls=true 与 tlsCAFile 同时填时，
// CA 池可能被一个空 tls.Config 覆盖，表现为偶发的
// 「certificate signed by unknown authority」——重启一次可能又好了。
func TestApplyMongoOptions_TLSParamsAreAggregated(t *testing.T) {
	// 造一个合法的 PEM（自签证书内容合法即可，不需要真的用它建连）
	caPath := writeTempCA(t)

	// 两种顺序都要验证：map 遍历顺序不确定，这里显式用两个键，
	// 反复构造 map 让运行期可能命中不同顺序。
	for i := 0; i < 20; i++ {
		opts := newClientOptionsForTest()
		err := applyMongoOptions(opts, map[string]string{
			"tls":       "true",
			"tlsCAFile": caPath,
		})
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		cfg := opts.TLSConfig
		if cfg == nil {
			t.Fatalf("round %d: TLS 应启用但配置为空", i)
		}
		if cfg.RootCAs == nil {
			t.Fatalf("round %d: CA 池丢失——说明 tls=true 覆盖了 tlsCAFile 的结果", i)
		}
	}
}

// tlsInsecure 与 tlsCAFile 组合时两者都要生效。
func TestApplyMongoOptions_TLSInsecureWithCA(t *testing.T) {
	caPath := writeTempCA(t)
	opts := newClientOptionsForTest()
	if err := applyMongoOptions(opts, map[string]string{
		"tlsCAFile":   caPath,
		"tlsInsecure": "true",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	cfg := opts.TLSConfig
	if cfg == nil {
		t.Fatal("TLS 应启用")
	}
	if !cfg.InsecureSkipVerify {
		t.Error("tlsInsecure=true 未生效")
	}
	if cfg.RootCAs == nil {
		t.Error("CA 池丢失")
	}
}

// 显式 tls=false 且没有其他 TLS 参数时，应当干净地关闭 TLS。
// （若同时给了 CA 文件，那是配置矛盾，见 TestApplyMongoOptions_TLSFalseConflictsWithCA）
func TestApplyMongoOptions_TLSFalseDisables(t *testing.T) {
	opts := newClientOptionsForTest()
	if err := applyMongoOptions(opts, map[string]string{"tls": "false"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opts.TLSConfig != nil {
		t.Error("tls=false 时必须关闭 TLS")
	}
}

// 完全没提 TLS 时保持驱动默认（不设置 TLSConfig）。
func TestApplyMongoOptions_NoTLSSettingKeepsDefault(t *testing.T) {
	opts := newClientOptionsForTest()
	if err := applyMongoOptions(opts, map[string]string{"replicaSet": "rs0"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if opts.TLSConfig != nil {
		t.Error("未提及 TLS 时不应改动驱动的默认行为")
	}
}

// 空值键要忽略，不能因为界面上留空就报「不支持该参数」。
func TestApplyMongoOptions_EmptyValuesIgnored(t *testing.T) {
	opts := newClientOptionsForTest()
	if err := applyMongoOptions(opts, map[string]string{
		"replicaSet": "",
		"tls":        "",
		"appName":    "",
	}); err != nil {
		t.Fatalf("empty values should be ignored: %v", err)
	}
}

// 未知键必须报错，且点名是哪个键。
func TestApplyMongoOptions_UnknownKey(t *testing.T) {
	opts := newClientOptionsForTest()
	err := applyMongoOptions(opts, map[string]string{"connectTimeoutMS": "1000"})
	if err == nil {
		t.Fatal("unknown key must be rejected")
	}
	if !strings.Contains(err.Error(), "connectTimeoutMS") {
		t.Errorf("error should name the key, got: %v", err)
	}
}

// writeTempCA 写一个合法的自签 CA 证书到临时文件，返回路径。
//
// 用真实生成的证书而不是随便一段文本：AppendCertsFromPEM 会校验格式，
// 假的 PEM 会让测试通过不了，反而掩盖逻辑问题。
func writeTempCA(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	path := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	return path
}

// tls=false 与 CA/inserure 同时出现是配置矛盾，必须报错而不是静默忽略：
// 静默忽略会让用户以为证书生效了，实际是明文连接。
func TestApplyMongoOptions_TLSFalseConflictsWithCA(t *testing.T) {
	caPath := writeTempCA(t)
	opts := newClientOptionsForTest()
	err := applyMongoOptions(opts, map[string]string{
		"tls":       "false",
		"tlsCAFile": caPath,
	})
	if err == nil {
		t.Fatal("tls=false + tlsCAFile 应当报冲突")
	}
	if !strings.Contains(err.Error(), "冲突") {
		t.Errorf("error should explain the conflict, got: %v", err)
	}
	// tlsInsecure 同理
	err = applyMongoOptions(newClientOptionsForTest(), map[string]string{
		"tls":         "false",
		"tlsInsecure": "true",
	})
	if err == nil {
		t.Fatal("tls=false + tlsInsecure 应当报冲突")
	}
}

// --- 集合名拆库：库名可以含点 ---
//
// 为什么要测：库名本身允许带点（"system.js"、"my.data"），
// 只有集合名不允许含点。所以必须用**最后一个**点号切分。
// 若误用第一个点号，"system.js.slowlog" 会被拆成库 "system"、集合 "js.slowlog"，
// 执行层就会连到错误的库。
func TestSplitDBCollection(t *testing.T) {
	cases := []struct {
		coll, database   string
		wantDB, wantColl string
		wantErr          bool
		why              string
	}{
		{"users", "shop", "shop", "users", false, "无点号：库用请求里的 database"},
		{"shop.users", "test", "shop", "users", false, "带库前缀，前缀优先于 database"},
		{"system.js.slowlog", "", "system.js", "slowlog", false, "库名含点，必须用最后一个点号切"},
		{"my.data.events", "", "my.data", "events", false, "同上，多点库名"},
		{"users", "", "", "users", false, "库为空：由调用方报错，拆分本身不拦"},
		{"shop.", "", "shop", "", true, "集合名为空必须报错"},
	}
	for _, c := range cases {
		db, coll, err := SplitDBCollection(c.coll, c.database)
		if c.wantErr {
			if err == nil {
				t.Errorf("SplitDBCollection(%q,%q) 应当报错", c.coll, c.database)
			}
			continue
		}
		if err != nil {
			t.Errorf("SplitDBCollection(%q,%q) 错误: %v", c.coll, c.database, err)
			continue
		}
		if db != c.wantDB || coll != c.wantColl {
			t.Errorf("SplitDBCollection(%q,%q) = (%q,%q), want (%q,%q) — %s",
				c.coll, c.database, db, coll, c.wantDB, c.wantColl, c.why)
		}
	}
}

// CA 文件必须有读取上限：tlsCAFile 的路径来自数据源配置，
// 填成 /dev/zero 或某个大日志文件时，无限读会一路吃到 OOM。
//
// 回归测试：早先 readCACert 写了上限但 applyMongoOptions 仍在用
// os.ReadFile，上限完全没生效——2MiB 文件会被整块读进内存后才报 PEM 错。
func TestApplyMongoOptions_RejectsOversizedCA(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/huge.pem"
	// 造一个超过 1MiB 的文件。内容是合法 PEM 前缀也没关系，
	// 因为上限检查在读完之后、解析之前就要拦下。
	blob := make([]byte, maxCACertSize+1)
	for i := range blob {
		blob[i] = 'A'
	}
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	opts := newClientOptionsForTest()
	err := applyMongoOptions(opts, map[string]string{"tlsCAFile": path})
	if err == nil {
		t.Fatal("超过上限的 CA 文件必须被拒绝")
	}
	if !strings.Contains(err.Error(), "超过") {
		t.Errorf("错误应提到文件过大，实际: %v", err)
	}
}

// readCACert 本身的上限行为。
func TestReadCACert_Limit(t *testing.T) {
	dir := t.TempDir()

	// 正常大小的合法证书应当能读
	ok := writeTempCA(t)
	if _, err := readCACert(ok); err != nil {
		t.Errorf("合法证书应可读: %v", err)
	}

	// 超大文件必须拒绝
	big := dir + "/big.pem"
	if err := os.WriteFile(big, make([]byte, maxCACertSize+1), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := readCACert(big); err == nil {
		t.Error("超过 maxCACertSize 必须报错")
	}

	// 不存在的文件
	if _, err := readCACert(dir + "/nope.pem"); err == nil {
		t.Error("不存在的文件必须报错")
	}
}
