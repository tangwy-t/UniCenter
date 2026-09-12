package ws

import (
	"bytes"
	"encoding/json"
	"testing"
)

// wantJSON 比较构造器输出与预期报文字节。
func wantJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	if !bytes.Equal(bytes.TrimSpace(got), []byte(want)) {
		t.Fatalf("got  = %s\nwant = %s", got, want)
	}
}

// TestServerMessageMarshals 服务端消息构造器输出必须与旧契约逐字节一致:
// auth_ok 的 user_id 保持字符串(雪花 ID 超 JS 安全整数),
// pong 无 data 键,new_notice 的 data 原样嵌入 JSON 对象。
func TestServerMessageMarshals(t *testing.T) {
	tests := []struct {
		name string
		got  func(t *testing.T) []byte
		want string
	}{
		{
			name: "auth_ok",
			got: func(t *testing.T) []byte {
				t.Helper()
				b, err := MarshalAuthOK(9007199254740993)
				if err != nil {
					t.Fatalf("MarshalAuthOK: %v", err)
				}
				return b
			},
			want: `{"type":"auth_ok","data":{"user_id":"9007199254740993"}}`,
		},
		{
			name: "auth_err",
			got: func(t *testing.T) []byte {
				t.Helper()
				b, err := MarshalAuthErr("token invalid")
				if err != nil {
					t.Fatalf("MarshalAuthErr: %v", err)
				}
				return b
			},
			want: `{"type":"auth_err","data":{"message":"token invalid"}}`,
		},
		{
			name: "kicked",
			got: func(t *testing.T) []byte {
				t.Helper()
				b, err := MarshalKicked("您的账号在其他设备登录")
				if err != nil {
					t.Fatalf("MarshalKicked: %v", err)
				}
				return b
			},
			want: `{"type":"kicked","data":{"message":"您的账号在其他设备登录"}}`,
		},
		{
			name: "pong",
			got: func(t *testing.T) []byte {
				t.Helper()
				b, err := MarshalPong()
				if err != nil {
					t.Fatalf("MarshalPong: %v", err)
				}
				return b
			},
			want: `{"type":"pong"}`,
		},
		{
			name: "new_notice",
			got: func(t *testing.T) []byte {
				t.Helper()
				notice := json.RawMessage(`{"id":1,"title":"标题"}`)
				b, err := MarshalNewNotice(notice)
				if err != nil {
					t.Fatalf("MarshalNewNotice: %v", err)
				}
				return b
			},
			want: `{"type":"new_notice","data":{"id":1,"title":"标题"}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wantJSON(t, tt.got(t), tt.want)
		})
	}
}
