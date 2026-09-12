package service

import (
	"bytes"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// 本文件是「存储型 cr0ss-site scripting」链（评审报告 #1）修复后的回归验证。
//
// 修复内容（详见 docs/security-review.md）：
//   1. 默认上传白名单移除活动内容扩展名（.html/.htm/.svg/.js/.mjs/.jsx/.tsx/.vue）。
//   2. detectFileMime 改为按文件真实内容（魔数）嗅探，不再取信客户端 Content-Type，
//      且回退不落到 text/html 等活动内容类型。
//   3. Preview 恒加 X-Content-Type-Options: nosniff + CSP default-src 'none'；
//      活动内容类型一律 Content-Disposition: attachment 强制下载。
//
// 以下断言固化修复后的安全行为，防止回归。

// 修复后 detectFileMime 不再取信客户端 Content-Type，而是按真实内容嗅探。
// 伪装成 png 的 HTML 内容会被嗅探为 text/html 而非客户端声明的 image/png。
func TestPoc_DetectFileMime_SniffsContentNotClientHeader(t *testing.T) {
	h := newPocFileHeaderWithContent("innocent.png", "image/png", []byte("<html><script>alert(1)</script></html>"))
	got := detectFileMime(h)
	if !strings.HasPrefix(got, "text/html") {
		t.Fatalf("应按真实内容嗅探为 text/html，实际 %q", got)
	}
	if util.IsInlineSafeMime(got) {
		t.Fatalf("text/html 应被判定为不可内联渲染，实际被判定为安全")
	}
}

// 修复后无法确定安全类型时回退为 octet-stream，而非 text/html。
func TestPoc_DetectFileMime_NeverFallsBackToActiveContent(t *testing.T) {
	h := newPocFileHeaderWithContent("evil.html", "", []byte{})
	got := detectFileMime(h)
	if strings.HasPrefix(got, "text/html") {
		t.Fatalf("修复后不应回退为 text/html，实际 %q", got)
	}
}

// newPocFileHeaderWithContent 构造带真实内容的 multipart FileHeader，供魔数嗅探。
func newPocFileHeaderWithContent(filename, contentType string, content []byte) *multipart.FileHeader {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, _ := w.CreateFormFile("files", filename)
	_, _ = fw.Write(content)
	_ = w.Close()

	reader := multipart.NewReader(&buf, w.Boundary())
	form, _ := reader.ReadForm(10 << 20)
	if files := form.File["files"]; len(files) > 0 {
		files[0].Header.Set("Content-Type", contentType)
		files[0].Filename = filename
		return files[0]
	}
	h := &multipart.FileHeader{Filename: filename, Header: make(textproto.MIMEHeader)}
	h.Header.Set("Content-Type", contentType)
	return h
}
