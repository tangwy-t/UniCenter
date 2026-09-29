package dockerops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
	"gopkg.in/yaml.v3"
)

// ── 四期配置编辑：三条写路径的验收测试 ────────────────────────────────────
//
// 用例覆盖 spec §8 四条防护与 §9 的合并语义：
//   - patch 的「未触碰段落逐字节保留」（含注释、空行、行尾注释）；
//   - null 的删除语义（删键 / 删整个服务）；
//   - 乐观锁拒绝、「文件已被他人修改，请刷新」；
//   - 备份保留 10 份与同秒顺延（不覆盖历史）；
//   - 回滚的内容与备份 hash 一致；
//   - 预检失败透传 stderr 首行且原文件一个字节没动；
//   - 原子写失败不留半成品；1MB 上限；令牌形态（路径分隔符一律拒）。

// composeEditorDoc 是四期用例的公共配置：顶层注释、服务内注释、行尾注释、块序列、
// 条目之间的空行都在里面 —— 「未触碰段落逐字节保留」的每一类载体都要有落点。
const composeEditorDoc = `# 顶层注释
services:
  # web 的说明
  web:
    image: nginx:alpine   # 行尾注释
    cpus: "1.0"
    ports:
      - "80:80"

  db:
    image: mysql:8.0
    environment:
      TZ: Asia/Shanghai
volumes:
  db-data:
`

// composeEditorFlowDoc 是流式写法（`cache: {…}`）：整段的逐字节保留做不到，
// 但兜底重建必须只重排那一段，其余字节不动。
const composeEditorFlowDoc = `services:
  web:
    image: nginx
  cache: {image: redis, restart: always}
`

func writeComposeDoc(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// newComposeFileExecutor 造一个指向 configFile 的写执行器（挂钟固定，令牌可预期）。
func newComposeFileExecutor(t *testing.T, configFile string, stub *composeExecStub, now time.Time) *WriteExecutor {
	t.Helper()
	e := NewWriteExecutor(composeProjectFixture(configFile), ParseProtected(""), t.TempDir(),
		agentproto.DockerComposeFlavorPlugin, stub.command)
	e.SetNow(func() time.Time { return now })
	return e
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// tempLeftovers 找执行后残留的手术临时文件（任何一条路径都不该留它）。
func tempLeftovers(t *testing.T, configFile string) []string {
	t.Helper()
	names, err := filepath.Glob(filepath.Join(filepath.Dir(configFile), composeTempPrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func mustExecError(t *testing.T, err error) *ExecError {
	t.Helper()
	if err == nil {
		t.Fatal("应失败，实际成功")
	}
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("错误必须是结论句形态 *ExecError，实际 %T: %v", err, err)
	}
	return ee
}

func decodeComposePayload(t *testing.T, payload []byte) *agentproto.DockerComposeFilePayload {
	t.Helper()
	var p agentproto.DockerComposeFilePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("结果载荷不是合法的 compose 文件载荷: %v", err)
	}
	return &p
}

// ── validate：同目录临时文件 + 透传 stderr 首行 ───────────────────────────

func TestComposeFileValidateUsesSameDirTempFile(t *testing.T) {
	configFile := writeComposeDoc(t, composeEditorDoc)
	stub := &composeExecStub{}
	e := newComposeFileExecutor(t, configFile, stub, time.Unix(1790000000, 0))

	content := "services:\n  web:\n    image: nginx:1.27\n"
	var seenTmp, seenContent string
	execFn := func(ctx context.Context, name string, args ...string) *exec.Cmd {
		seenTmp = args[2]
		b, err := os.ReadFile(seenTmp)
		if err != nil {
			t.Errorf("预检时临时文件必须存在（compose 要读它）: %v", err)
		}
		seenContent = string(b)
		return stub.command(ctx, name, args...)
	}
	e.exec = execFn

	payload, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action:  agentproto.DockerActionComposeFileValidate,
		Options: agentproto.DockerCmdOptions{Target: "uni-center", Content: content},
	})
	if err != nil {
		t.Fatalf("预检应通过: %v", err)
	}
	if payload != nil {
		t.Fatalf("预检不产生数据面，实际 %s", payload)
	}
	call := stub.last(t)
	if call.name != "docker" || call.args[0] != "compose" {
		t.Fatalf("plugin 形态必须是 docker compose: %q", call.args)
	}
	if got := call.args[1:]; len(got) != 4 || got[0] != "-f" || got[2] != "config" || got[3] != "-q" {
		t.Fatalf("argv 必须逐字固定为 -f <tmp> config -q，实际 %q", call.args)
	}
	if filepath.Dir(seenTmp) != filepath.Dir(configFile) {
		t.Fatalf("临时文件必须与配置文件同目录（相对路径与 .env 的解析基准）: %s vs %s",
			filepath.Dir(seenTmp), filepath.Dir(configFile))
	}
	if seenContent != content {
		t.Fatalf("临时文件内容必须是待校验的正文: %q", seenContent)
	}
	if _, err := os.Stat(seenTmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("预检结束后临时文件必须删除，实际 err=%v", err)
	}
	if readFileString(t, configFile) != composeEditorDoc {
		t.Fatal("预检不得改动原文件")
	}
}

func TestComposeFileValidatePropagatesStderrFirstLine(t *testing.T) {
	configFile := writeComposeDoc(t, composeEditorDoc)
	stub := &composeExecStub{exitCode: 1,
		stderr: "services.web.image is required\nsecond line: details"}
	e := newComposeFileExecutor(t, configFile, stub, time.Unix(1790000000, 0))

	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeFileValidate,
		Options: agentproto.DockerCmdOptions{Target: "uni-center",
			Content: "services:\n  web:\n    image:\n"},
	})
	ee := mustExecError(t, err)
	if ee.Msg != "services.web.image is required" {
		t.Fatalf("结论句必须是 stderr 首行（透传），实际 %q", ee.Msg)
	}
	if !strings.Contains(ee.Detail, "second line") {
		t.Fatalf("完整输出必须进 detail，实际 %q", ee.Detail)
	}
	if readFileString(t, configFile) != composeEditorDoc {
		t.Fatal("预检失败不得改动原文件")
	}
	if left := tempLeftovers(t, configFile); len(left) != 0 {
		t.Fatalf("预检失败不得留下临时文件: %v", left)
	}
}

// ── write：乐观锁 / 全文写 / 回滚 ─────────────────────────────────────────

func TestComposeFileWriteOptimisticLockRejects(t *testing.T) {
	configFile := writeComposeDoc(t, composeEditorDoc)
	stub := &composeExecStub{}
	e := newComposeFileExecutor(t, configFile, stub, time.Unix(1790000000, 0))

	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeFileWrite,
		Options: agentproto.DockerCmdOptions{Target: "uni-center",
			Content:  "services:\n  web:\n    image: nginx:1.27\n",
			BaseHash: strings.Repeat("0", 64)},
	})
	ee := mustExecError(t, err)
	if ee.Msg != "文件已被他人修改，请刷新" {
		t.Fatalf("乐观锁的结论句必须与 spec 一字不差，实际 %q", ee.Msg)
	}
	if !strings.Contains(ee.Detail, sha256Hex([]byte(composeEditorDoc))) {
		t.Fatalf("detail 要带当前 hash 便于排障/刷新，实际 %q", ee.Detail)
	}
	if stub.count() != 0 {
		t.Fatal("乐观锁拒绝必须在预检之前发生（不得触达 CLI）")
	}
	if readFileString(t, configFile) != composeEditorDoc {
		t.Fatal("被拒的保存不得改动文件")
	}
	if tokens, err := composeBackupNames(configFile); err != nil || len(tokens) != 0 {
		t.Fatalf("被拒的保存不得产生备份: %v %v", tokens, err)
	}
}

func TestComposeFileWriteAndRollback(t *testing.T) {
	configFile := writeComposeDoc(t, composeEditorDoc)
	stub := &composeExecStub{}
	e := newComposeFileExecutor(t, configFile, stub, time.Unix(1790000000, 0))
	ctx := context.Background()

	newContent := "# 新内容\nservices:\n  web:\n    image: nginx:1.27\n"
	payload, err := e.Do(ctx, &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeFileWrite,
		Options: agentproto.DockerCmdOptions{Target: "uni-center", Content: newContent,
			BaseHash: sha256Hex([]byte(composeEditorDoc))},
	})
	if err != nil {
		t.Fatalf("全文写应成功: %v", err)
	}
	p := decodeComposePayload(t, payload)
	if p.Content != newContent || p.Hash != sha256Hex([]byte(newContent)) || p.Path != configFile {
		t.Fatalf("保存的返回载荷必须是回读后的唯一事实源: %+v", p)
	}
	if len(p.Backups) != 1 {
		t.Fatalf("保存前必须落一份备份，实际 %+v", p.Backups)
	}
	if p.Backups[0].Hash != sha256Hex([]byte(composeEditorDoc)) ||
		p.Backups[0].SizeBytes != int64(len(composeEditorDoc)) {
		t.Fatalf("备份条目必须带原文件的 hash/size: %+v", p.Backups[0])
	}
	if got := readFileString(t, configFile); got != newContent {
		t.Fatalf("文件必须被替换为新正文:\n%s", got)
	}
	token := p.Backups[0].Token
	if got := readFileString(t, composeBackupPath(configFile, token)); got != composeEditorDoc {
		t.Fatal("备份内容必须是保存前的原文")
	}

	// 回滚：正文来源换成备份令牌（不带 content），乐观锁对回滚同样成立。
	payload2, err := e.Do(ctx, &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeFileWrite,
		Options: agentproto.DockerCmdOptions{Target: "uni-center", Backup: token,
			BaseHash: sha256Hex([]byte(newContent))},
	})
	if err != nil {
		t.Fatalf("回滚应成功: %v", err)
	}
	p2 := decodeComposePayload(t, payload2)
	if p2.Content != composeEditorDoc || p2.Hash != sha256Hex([]byte(composeEditorDoc)) {
		t.Fatal("回滚后的内容必须与备份逐字节一致（hash 相等）")
	}
	if got := readFileString(t, configFile); got != composeEditorDoc {
		t.Fatal("回滚必须写回原文件")
	}
	if len(p2.Backups) != 2 || p2.Backups[0].Token == token {
		t.Fatalf("回滚本身也会落一份备份（回滚可再回滚）: %+v", p2.Backups)
	}
	if p2.Backups[0].Hash != sha256Hex([]byte(newContent)) {
		t.Fatalf("最新备份应是回滚前的正文: %+v", p2.Backups[0])
	}
	if p2.Backups[1].Token != token {
		t.Fatalf("备份列表按时间倒序: %+v", p2.Backups)
	}
}

func TestComposeFileWriteRejectsUnknownBackup(t *testing.T) {
	configFile := writeComposeDoc(t, composeEditorDoc)
	stub := &composeExecStub{}
	e := newComposeFileExecutor(t, configFile, stub, time.Unix(1790000000, 0))

	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeFileWrite,
		Options: agentproto.DockerCmdOptions{Target: "uni-center", Backup: "20260928-153000",
			BaseHash: sha256Hex([]byte(composeEditorDoc))},
	})
	ee := mustExecError(t, err)
	if !strings.Contains(ee.Msg, "找不到这份备份") {
		t.Fatalf("缺失备份要给出可照做的结论句，实际 %q", ee.Msg)
	}
	if readFileString(t, configFile) != composeEditorDoc {
		t.Fatal("找不到备份不得改动文件")
	}
}

// 令牌是协议上唯一能指到文件系统的现场输入：路径分隔符等形态必须在**执行之前**拒。
func TestComposeFileBackupTokenShapeRejected(t *testing.T) {
	configFile := writeComposeDoc(t, composeEditorDoc)
	stub := &composeExecStub{}
	e := newComposeFileExecutor(t, configFile, stub, time.Unix(1790000000, 0))

	for _, token := range []string{
		"../../etc/passwd",
		"20260928-153000/../../x",
		"20260928-153000\x00",
		"20260928153000",
		"../20260928-153000",
	} {
		_, err := e.Do(context.Background(), &agentproto.DockerCmd{
			Action: agentproto.DockerActionComposeFileWrite,
			Options: agentproto.DockerCmdOptions{Target: "uni-center", Backup: token,
				BaseHash: sha256Hex([]byte(composeEditorDoc))},
		})
		ee := mustExecError(t, err)
		if ee.Msg != "备份令牌不合法" {
			t.Fatalf("令牌 %q 必须被结论句拒绝，实际 %q", token, ee.Msg)
		}
	}
	if stub.count() != 0 {
		t.Fatal("非法令牌不得触达 CLI")
	}
	if readFileString(t, configFile) != composeEditorDoc {
		t.Fatal("非法令牌不得改动文件")
	}
}

// ── patch：未触碰段落逐字节保留 + null 删除语义 ───────────────────────────

// patchOnce 跑一次 patch 并返回新文本（断言整份文件的字节，而不是某一段）。
func patchOnce(t *testing.T, doc string, patch map[string]any) string {
	t.Helper()
	configFile := writeComposeDoc(t, doc)
	stub := &composeExecStub{}
	e := newComposeFileExecutor(t, configFile, stub, time.Unix(1790000000, 0))
	payload, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeFilePatch,
		Options: agentproto.DockerCmdOptions{Target: "uni-center", Patch: patch,
			BaseHash: sha256Hex([]byte(doc))},
	})
	if err != nil {
		t.Fatalf("patch 应成功: %v", err)
	}
	p := decodeComposePayload(t, payload)
	onDisk := readFileString(t, configFile)
	if p.Content != onDisk || p.Hash != sha256Hex([]byte(onDisk)) {
		t.Fatalf("patch 的返回载荷必须与磁盘内容一致（唯一事实源）")
	}
	if left := tempLeftovers(t, configFile); len(left) != 0 {
		t.Fatalf("patch 不得留下临时文件: %v", left)
	}
	// 产物必须仍是合法 YAML：真实环境由 config -q 兜底，但逐字节手术的 bug 在测试里
	// 就该现形（而不是等到生产预检的一行英文错误）。
	var probe any
	if err := yaml.Unmarshal([]byte(onDisk), &probe); err != nil {
		t.Fatalf("patch 产物不是合法 YAML: %v\n%s", err, onDisk)
	}
	return onDisk
}

func TestComposeFilePatchKeepsUntouchedBytes(t *testing.T) {
	t.Run("改标量只动那一个 token", func(t *testing.T) {
		got := patchOnce(t, composeEditorDoc, map[string]any{
			"services": map[string]any{"web": map[string]any{"cpus": "2.0"}},
		})
		want := strings.Replace(composeEditorDoc, `cpus: "1.0"`, `cpus: "2.0"`, 1)
		if got != want {
			t.Fatalf("未触碰段落必须逐字节保留:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("带行尾注释的标量保留注释", func(t *testing.T) {
		got := patchOnce(t, composeEditorDoc, map[string]any{
			"services": map[string]any{"web": map[string]any{"image": "nginx:1.27"}},
		})
		want := strings.Replace(composeEditorDoc,
			"image: nginx:alpine   # 行尾注释", "image: nginx:1.27   # 行尾注释", 1)
		if got != want {
			t.Fatalf("行尾注释必须原样留下:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("替换块序列保留其它行", func(t *testing.T) {
		got := patchOnce(t, composeEditorDoc, map[string]any{
			"services": map[string]any{"web": map[string]any{"ports": []any{"8080:80"}}},
		})
		want := strings.Replace(composeEditorDoc,
			"    ports:\n      - \"80:80\"\n", "    ports:\n      - 8080:80\n", 1)
		if got != want {
			t.Fatalf("块序列替换不得牵动相邻行（含其后的空行）:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("同行标量改成集合落成块", func(t *testing.T) {
		doc := "services:\n  web:\n    image: nginx\n    ports: [\"80:80\"]   # 端口\n"
		got := patchOnce(t, doc, map[string]any{
			"services": map[string]any{"web": map[string]any{"ports": []any{"8080:80"}}},
		})
		want := "services:\n  web:\n    image: nginx\n    ports:\n      - 8080:80\n"
		if got != want {
			t.Fatalf("同行标量→集合必须落成块且不残留旧行尾注释:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("块集合改成标量", func(t *testing.T) {
		doc := "services:\n  web:\n    image: nginx\n    ports:\n      - \"80:80\"\n"
		got := patchOnce(t, doc, map[string]any{
			"services": map[string]any{"web": map[string]any{"ports": "none"}},
		})
		want := "services:\n  web:\n    image: nginx\n    ports:\n      none\n"
		if got != want {
			t.Fatalf("块集合→标量必须撤回单行:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("新增键插在该服务末尾", func(t *testing.T) {
		got := patchOnce(t, composeEditorDoc, map[string]any{
			"services": map[string]any{"web": map[string]any{"restart": "unless-stopped"}},
		})
		want := strings.Replace(composeEditorDoc,
			"      - \"80:80\"\n", "      - \"80:80\"\n    restart: unless-stopped\n", 1)
		if got != want {
			t.Fatalf("新增键必须落在该服务块内、其余字节不动:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("新增整个服务", func(t *testing.T) {
		got := patchOnce(t, composeEditorDoc, map[string]any{
			"services": map[string]any{"redis": map[string]any{
				"image": "redis:7-alpine", "restart": "unless-stopped",
				"ports": []any{"6379:6379"}}},
		})
		block := "  redis:\n" +
			"    image: redis:7-alpine\n" +
			"    ports:\n" +
			"      - 6379:6379\n" +
			"    restart: unless-stopped\n"
		want := strings.Replace(composeEditorDoc, "volumes:\n", block+"volumes:\n", 1)
		if got != want {
			t.Fatalf("新增服务必须插在 services 末尾（键序稳定）:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("null 删键", func(t *testing.T) {
		got := patchOnce(t, composeEditorDoc, map[string]any{
			"services": map[string]any{"web": map[string]any{"cpus": nil}},
		})
		want := strings.Replace(composeEditorDoc, "    cpus: \"1.0\"\n", "", 1)
		if got != want {
			t.Fatalf("null 必须删除该键:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("null 删整个服务", func(t *testing.T) {
		dbBlock := "  db:\n    image: mysql:8.0\n    environment:\n      TZ: Asia/Shanghai\n"
		got := patchOnce(t, composeEditorDoc, map[string]any{
			"services": map[string]any{"db": nil},
		})
		want := strings.Replace(composeEditorDoc, dbBlock, "", 1)
		if got != want {
			t.Fatalf("null 服务必须整段删除（含其内容）:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("流式服务整段重建但不牵动邻段", func(t *testing.T) {
		got := patchOnce(t, composeEditorFlowDoc, map[string]any{
			"services": map[string]any{"cache": map[string]any{"image": "redis:7"}},
		})
		block := "  cache:\n    image: redis:7\n    restart: always\n"
		want := strings.Replace(composeEditorFlowDoc,
			"  cache: {image: redis, restart: always}\n", block, 1)
		if got != want {
			t.Fatalf("流式容器走兜底重建，web 段必须完全不动:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("新增段落追加在文件末尾", func(t *testing.T) {
		got := patchOnce(t, composeEditorDoc, map[string]any{
			"networks": map[string]any{"app-net": map[string]any{"driver": "bridge"}},
		})
		want := composeEditorDoc + "networks:\n  app-net:\n    driver: bridge\n"
		if got != want {
			t.Fatalf("新增段落必须追加且原有内容逐字节不动:\n got %q\nwant %q", got, want)
		}
	})
}

// patch 的预检失败：透传 stderr 首行，且**文件一个字节没动**（不留半成品）。
func TestComposeFilePatchPrecheckFailureLeavesFileUntouched(t *testing.T) {
	configFile := writeComposeDoc(t, composeEditorDoc)
	stub := &composeExecStub{exitCode: 1,
		stderr: "yaml: line 3: mapping values are not allowed in this context"}
	e := newComposeFileExecutor(t, configFile, stub, time.Unix(1790000000, 0))

	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeFilePatch,
		Options: agentproto.DockerCmdOptions{Target: "uni-center",
			Patch:    map[string]any{"services": map[string]any{"web": map[string]any{"cpus": "2.0"}}},
			BaseHash: sha256Hex([]byte(composeEditorDoc))},
	})
	ee := mustExecError(t, err)
	if ee.Msg != "yaml: line 3: mapping values are not allowed in this context" {
		t.Fatalf("预检失败要透传 stderr 首行，实际 %q", ee.Msg)
	}
	if got := readFileString(t, configFile); got != composeEditorDoc {
		t.Fatalf("预检失败必须原文件不动:\n%s", got)
	}
	if tokens, _ := composeBackupNames(configFile); len(tokens) != 0 {
		t.Fatalf("预检在备份之前，不得产生备份: %v", tokens)
	}
	if left := tempLeftovers(t, configFile); len(left) != 0 {
		t.Fatalf("失败路径不得留下临时文件: %v", left)
	}
}

// ── 备份保留 10 份 / 同秒顺延 ─────────────────────────────────────────────

func TestComposeFileBackupRetentionAndSameSecond(t *testing.T) {
	configFile := writeComposeDoc(t, "services:\n  a:\n    image: x0\n")
	stub := &composeExecStub{}
	// 挂钟**固定不动**：连续保存全落在同一秒，令牌必须顺延而不是覆盖同名备份。
	e := newComposeFileExecutor(t, configFile, stub, time.Unix(1790000000, 0))
	ctx := context.Background()

	for i := 1; i <= 12; i++ {
		cur := readFileString(t, configFile)
		next := fmt.Sprintf("services:\n  a:\n    image: x%d\n", i)
		if _, err := e.Do(ctx, &agentproto.DockerCmd{
			Action: agentproto.DockerActionComposeFileWrite,
			Options: agentproto.DockerCmdOptions{Target: "uni-center", Content: next,
				BaseHash: sha256Hex([]byte(cur))},
		}); err != nil {
			t.Fatalf("第 %d 次保存应成功: %v", i, err)
		}
	}

	tokens, err := composeBackupNames(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != maxComposeBackups {
		t.Fatalf("备份必须保留最近 %d 份，实际 %d 份: %v", maxComposeBackups, len(tokens), tokens)
	}
	seen := map[string]bool{}
	for _, tk := range tokens {
		if seen[tk] {
			t.Fatalf("同秒保存覆盖了同名备份（令牌必须顺延）: %v", tokens)
		}
		seen[tk] = true
	}
	// 倒序：第一份是最新的（第 12 次保存前的原文 x11）；最早的两次已被清理。
	if readFileString(t, composeBackupPath(configFile, tokens[0])) != "services:\n  a:\n    image: x11\n" {
		t.Fatalf("倒序第一份应是最新备份: %+v", tokens)
	}
	bs, err := listComposeBackups(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != maxComposeBackups || bs[0].Token != tokens[0] {
		t.Fatalf("列表口径必须与磁盘一致（最多 %d、倒序）: %+v", maxComposeBackups, bs)
	}
	if bs[0].Hash == "" || bs[0].SizeBytes <= 0 || bs[0].At <= 0 {
		t.Fatalf("备份条目必须带 hash/size/at: %+v", bs[0])
	}
}

// ── 尺寸上限 / 原子写失败 ────────────────────────────────────────────────

func TestComposeFileSizeLimit(t *testing.T) {
	configFile := writeComposeDoc(t, composeEditorDoc)
	stub := &composeExecStub{}
	e := newComposeFileExecutor(t, configFile, stub, time.Unix(1790000000, 0))

	_, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeFileWrite,
		Options: agentproto.DockerCmdOptions{Target: "uni-center",
			Content:  strings.Repeat("x", agentproto.MaxDockerComposeFileBytes+1),
			BaseHash: sha256Hex([]byte(composeEditorDoc))},
	})
	ee := mustExecError(t, err)
	if !strings.Contains(ee.Msg, "1MB") {
		t.Fatalf("超限必须给出尺寸结论句，实际 %q", ee.Msg)
	}

	// 当前文件本身超限：读都不该读进内存。
	big := writeComposeDoc(t, strings.Repeat("x", agentproto.MaxDockerComposeFileBytes+1))
	e2 := newComposeFileExecutor(t, big, stub, time.Unix(1790000000, 0))
	_, err = e2.Do(context.Background(), &agentproto.DockerCmd{
		Action: agentproto.DockerActionComposeFileWrite,
		Options: agentproto.DockerCmdOptions{Target: "uni-center", Content: "services: {}\n",
			BaseHash: strings.Repeat("0", 64)},
	})
	if ee := mustExecError(t, err); !strings.Contains(ee.Msg, "过大") {
		t.Fatalf("超限的现有文件必须被拒读，实际 %q", ee.Msg)
	}
	if readFileString(t, configFile) != composeEditorDoc {
		t.Fatal("超限的保存不得改动文件")
	}
}

// 原子写的失败路径：rename 失败时原目标不动、临时文件不留（半成品）。
func TestComposeAtomicWriteLeavesNoPartial(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target-is-a-dir")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFileAtomic(target, []byte("data"), 0o644); err == nil {
		t.Fatal("目标不可替换（是目录）时必须报错")
	}
	fi, err := os.Stat(target)
	if err != nil || !fi.IsDir() {
		t.Fatalf("失败后原目标必须原样保留: %v %v", fi, err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, composeTempPrefix+"*"))
	if len(left) != 0 {
		t.Fatalf("rename 失败不得留下临时文件: %v", left)
	}
	// 成功路径：内容与权限位落盘。
	ok := filepath.Join(dir, "ok.yml")
	if err := writeFileAtomic(ok, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err = os.Stat(ok)
	if err != nil || fi.Mode().Perm() != 0o600 || readFileString(t, ok) != "services: {}\n" {
		t.Fatalf("原子写的内容/权限不符: %v %v", fi, err)
	}
}

// ── read：备份列表 ──────────────────────────────────────────────────────

func TestComposeFileReadListsBackups(t *testing.T) {
	configFile := writeComposeDoc(t, composeEditorDoc)
	if err := os.WriteFile(composeBackupPath(configFile, "20260928-120000"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(composeBackupPath(configFile, "20260928-130000"), []byte("newer"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 同前缀但不是令牌的文件不是备份：不列（也不该被清理逻辑碰）。
	if err := os.WriteFile(filepath.Join(filepath.Dir(configFile), "docker-compose.yml.bak-notatoken"),
		[]byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	e := NewReadExecutor(composeProjectFixture(configFile))
	payload, err := e.Do(context.Background(), &agentproto.DockerCmd{
		Action:  agentproto.DockerActionComposeFileRead,
		Options: agentproto.DockerCmdOptions{Target: "uni-center"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var p agentproto.DockerComposeFilePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatal(err)
	}
	if p.Content != composeEditorDoc || p.Hash != sha256Hex([]byte(composeEditorDoc)) {
		t.Fatal("读取的正文/hash 必须与文件一致")
	}
	if len(p.Backups) != 2 {
		t.Fatalf("应列出两份备份，实际 %+v", p.Backups)
	}
	if p.Backups[0].Token != "20260928-130000" || p.Backups[1].Token != "20260928-120000" {
		t.Fatalf("备份列表必须按时间倒序: %+v", p.Backups)
	}
	if p.Backups[0].Hash != sha256Hex([]byte("newer")) || p.Backups[0].SizeBytes != int64(len("newer")) ||
		p.Backups[0].At <= 0 {
		t.Fatalf("备份条目必须带各自的 hash/size/at: %+v", p.Backups[0])
	}
}

// ── dispatcher：三条 action 已放行且走同一执行器 ──────────────────────────

func TestComposeFileActionsImplementedAndDispatched(t *testing.T) {
	for _, a := range []string{
		agentproto.DockerActionComposeFileValidate,
		agentproto.DockerActionComposeFileWrite,
		agentproto.DockerActionComposeFilePatch,
	} {
		if !implementedActions[a] {
			t.Fatalf("%s 已由四期实现，必须登记为已实现", a)
		}
	}

	configFile := writeComposeDoc(t, composeEditorDoc)
	stub := &composeExecStub{}
	e := newComposeFileExecutor(t, configFile, stub, time.Unix(1790000000, 0))
	sink := &collectSink{}
	d := NewDispatcher(e, sink.send, testLogger())
	ctx := context.Background()

	written := "services:\n  web:\n    image: nginx:1.27\n"
	cmds := []*agentproto.DockerCmd{
		{Ref: "1", Action: agentproto.DockerActionComposeFileValidate,
			Options: agentproto.DockerCmdOptions{Target: "uni-center", Content: written}},
		{Ref: "2", Action: agentproto.DockerActionComposeFileWrite,
			Options: agentproto.DockerCmdOptions{Target: "uni-center", Content: written,
				BaseHash: sha256Hex([]byte(composeEditorDoc))},
			Confirm: "uni-center"},
		{Ref: "3", Action: agentproto.DockerActionComposeFilePatch,
			Options: agentproto.DockerCmdOptions{Target: "uni-center",
				Patch:    map[string]any{"services": map[string]any{"web": map[string]any{"cpus": "2.0"}}},
				BaseHash: sha256Hex([]byte(written))},
			Confirm: "uni-center"},
	}
	for _, cmd := range cmds {
		d.Handle(ctx, cmd)
		d.ExecuteNow(ctx, cmd.Ref)
	}
	got := sink.all()
	if len(got) != 3 {
		t.Fatalf("三条指令都必须有结果，实际 %d", len(got))
	}
	for i, r := range got {
		if !r.OK {
			t.Fatalf("第 %d 条被拒: %s / %s", i+1, r.Error, r.Detail)
		}
	}
	if got[0].Payload != nil {
		t.Fatalf("validate 不该有数据面: %s", got[0].Payload)
	}
	if len(got[1].Payload) == 0 || len(got[2].Payload) == 0 {
		t.Fatal("write/patch 必须回数据面（回读后的内容 + 备份列表）")
	}
	final := readFileString(t, configFile)
	if !strings.Contains(final, "image: nginx:1.27") || !strings.Contains(final, `cpus: "2.0"`) {
		t.Fatalf("dispatcher 路径上的写必须真的落盘:\n%s", final)
	}
}
