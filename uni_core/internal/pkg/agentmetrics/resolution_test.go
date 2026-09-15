package agentmetrics

import (
	"testing"
)

// TestCursorKeyMatchesSpecContract 用**字符串字面量**钉住键名契约，不复用 CursorKey
// 里的拼装（否则改错了也自洽）。
//
// 键名漂移不会报错，只会「静默读不到」：flush/rollup 各按自己的键名读写，
// 单测全绿，而运维按 spec 键名 DEL、外部脚本按 spec 键名读水位却找不到数据。
// 形态必须与 latestKey/historyKey 同源（agent:device:{id}:xxx），
// **不得**写成 agent:device:cursor_5m:{id} 之类的「名字在前」形态。
func TestCursorKeyMatchesSpecContract(t *testing.T) {
	if got := CursorKey(1001, Resolution5m); got != "agent:device:1001:cursor_5m" {
		t.Fatalf("CursorKey(1001, Resolution5m) = %q, want %q（spec §7 键名契约）", got, "agent:device:1001:cursor_5m")
	}
	if got := CursorKey(1001, Resolution1h); got != "agent:device:1001:cursor_1h" {
		t.Fatalf("CursorKey(1001, Resolution1h) = %q, want %q", got, "agent:device:1001:cursor_1h")
	}
	// 与 latest/history 的形态对照：前缀同源、设备号在档位名之前。
	if got := CursorKey(1001, Resolution5m); got != "agent:device:1001:"+cursorKeySuffixes[Resolution5m] {
		t.Fatalf("游标键 %q 与 latestKey/historyKey 不同源", got)
	}
	// 档位名必须落在设备号**之后**（历史缺陷：latest 写成 agent:device:latest:{id}）。
	if bad := "agent:device:cursor_5m:1001"; CursorKey(1001, Resolution5m) == bad {
		t.Fatalf("游标键 %q 用了「档位名在前」的旧形态", bad)
	}
	// 两个档位必须是**两个不同的键**：同键会让 flush 与 rollup 互相覆盖水位。
	if CursorKey(1001, Resolution5m) == CursorKey(1001, Resolution1h) {
		t.Fatal("cursor_5m 与 cursor_1h 不得是同一个键（两段提交各有一族水位）")
	}
}

func TestResolutionBucketSeconds(t *testing.T) {
	if got := Resolution5m.BucketSeconds(); got != 300 {
		t.Fatalf("Resolution5m.BucketSeconds() = %d, want 300", got)
	}
	if got := Resolution1h.BucketSeconds(); got != 3600 {
		t.Fatalf("Resolution1h.BucketSeconds() = %d, want 3600", got)
	}
	if Resolution5m.String() != "5m" || Resolution1h.String() != "1h" {
		t.Fatalf("Resolution.String() = %q/%q, want 5m/1h", Resolution5m, Resolution1h)
	}
}
