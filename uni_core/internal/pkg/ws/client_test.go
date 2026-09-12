package ws

import (
	"testing"

	"github.com/gorilla/websocket"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
)

// TestClientIdentityUsesContextkeys ws 客户端身份必须落在 contextkeys.UserID,
// 使任何读取 contextkeys.UserIDFromCtx 的服务对 ws 客户端上下文同样可用
// (与 HTTP 中间件注入键完全一致)。
func TestClientIdentityUsesContextkeys(t *testing.T) {
	c := NewClient(nil, &websocket.Conn{}, nil)
	if _, ok := contextkeys.UserIDFromCtx(c); ok {
		t.Fatal("未认证客户端不应携带 userID")
	}
	c.SetUserID(7)
	id, ok := contextkeys.UserIDFromCtx(c)
	if !ok || id != 7 {
		t.Fatalf("SetUserID 后 UserIDFromCtx = %d/%v, want 7/true", id, ok)
	}
	if c.UserID() != 7 {
		t.Fatalf("UserID() = %d, want 7", c.UserID())
	}
	if !c.IsAuthed() {
		t.Fatal("SetUserID 应置 authed 标记")
	}
}

// TestClientPrivateStateKeysDoNotLeakIntoIdentity ws 私有状态键不污染身份读取。
func TestClientPrivateStateKeysDoNotLeakIntoIdentity(t *testing.T) {
	c := NewClient(nil, &websocket.Conn{}, nil)
	c.SetUserID(1)
	c.SetKickToken("abc")
	// 绑定状态不得改变身份读取路径。
	if c.UserID() != 1 || c.KickToken() != "abc" {
		t.Fatal("身份与 kickToken 状态互相污染")
	}
}
