package migrations

import (
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// TestFileAllowedExtsRejectsActiveContent 是上传白名单的安全守卫：
// 默认白名单不得包含可在同源内联预览时执行脚本的「活动内容」扩展名。
// 这些后缀此前会让攻击者上传同源 HTML/SVG/JS 并经 Preview 内联渲染执行
// 脚本（存储型 XSS，评审 #1）。
func TestFileAllowedExtsRejectsActiveContent(t *testing.T) {
	allowed := util.ParseExtSet(fileAllowedExtsFull)
	activeExts := []string{".html", ".htm", ".svg", ".js", ".mjs", ".jsx", ".tsx", ".vue"}
	for _, ext := range activeExts {
		if allowed[ext] {
			t.Errorf("默认上传白名单不应包含活动内容扩展名 %s（可被 Preview 内联执行脚本）", ext)
		}
	}
	// 代码类文件仍应允许上传（.py/.go/.java/.c/.ts 等），仅预览强制下载。
	for _, ext := range []string{".py", ".go", ".java", ".c", ".cpp", ".ts"} {
		if !allowed[ext] {
			t.Errorf("代码类扩展名 %s 应保留在上传白名单（仅 Preview 强制下载）", ext)
		}
	}
}
