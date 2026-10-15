package agentproto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// golden 文件必须在**载荷层**也能解码并过校验。
//
// 为什么需要这条（而不是只靠 snapshot_test.go 的 TestGoldenFilesRoundTrip）：
// 那条只做「信封解码 + 信封字段回环」，看不见载荷形状错误。实测抓到过一例：
// 结果载荷被写成 JSON 对象而线上是 []byte（base64 串）—— 信封门禁全绿，真解码
// 时才炸。golden 是「规范线上形态」的样本，形状错了就是在教后来者写错的代码，
// 故在这一层再钉一次。
//
// DecodeTyped 连载荷的 Validate 一起跑，故它同时守住「样本是语义合法的」。
func TestGoldenPayloadsDecode(t *testing.T) {
	dir := filepath.Join("testdata", "golden", "v1")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("golden 目录不可读: %v", err)
	}
	checked := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		m, err := Decode(raw)
		if err != nil {
			t.Fatalf("%s: 信封解码失败: %v", e.Name(), err)
		}
		if _, err := DecodeTyped(m); err != nil {
			t.Fatalf("%s 的载荷无法解码或语义非法（golden 必须是真的线上形态）: %v", e.Name(), err)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("golden 目录里没有可检查的样本")
	}
	t.Logf("已检查 %d 个 golden 样本", checked)
}
