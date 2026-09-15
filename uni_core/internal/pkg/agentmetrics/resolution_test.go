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
	key5m, err := CursorKey(1001, Resolution5m)
	if err != nil {
		t.Fatalf("CursorKey(1001, Resolution5m) 报错: %v", err)
	}
	if key5m != "agent:device:1001:cursor_5m" {
		t.Fatalf("CursorKey(1001, Resolution5m) = %q, want %q（spec §7 键名契约）", key5m, "agent:device:1001:cursor_5m")
	}
	key1h, err := CursorKey(1001, Resolution1h)
	if err != nil {
		t.Fatalf("CursorKey(1001, Resolution1h) 报错: %v", err)
	}
	if key1h != "agent:device:1001:cursor_1h" {
		t.Fatalf("CursorKey(1001, Resolution1h) = %q, want %q", key1h, "agent:device:1001:cursor_1h")
	}
	// 与 latest/history 的形态对照：前缀同源、设备号在档位名之前。
	if key5m != "agent:device:1001:"+cursorKeySuffixes[Resolution5m] {
		t.Fatalf("游标键 %q 与 latestKey/historyKey 不同源", key5m)
	}
	// 档位名必须落在设备号**之后**（历史缺陷：latest 写成 agent:device:latest:{id}）。
	if bad := "agent:device:cursor_5m:1001"; key5m == bad {
		t.Fatalf("游标键 %q 用了「档位名在前」的旧形态", bad)
	}
	// 两个档位必须是**两个不同的键**：同键会让 flush 与 rollup 互相覆盖水位。
	if key5m == key1h {
		t.Fatal("cursor_5m 与 cursor_1h 不得是同一个键（两段提交各有一族水位）")
	}
}

// TestCursorKeyRejectsUnknownResolution 钉住「未登记档位**不得**静默退化成 5m」。
//
// 这条断言是注释与代码一致性的守卫（缺陷本体就是两者相反）：旧实现把未知值
// `idx = 0`（即 5m），于是 `CursorKey(id, 99) == CursorKey(id, Resolution5m)` ——
// 一个手滑传错的档位会把 rollup 的水位写进 flush 的键里，两个档位互相覆盖水位，
// 症状是「某些桶永远重算 / 永远漏算」，而且**完全不可观测**（键名看起来都对）。
//
// 反向验证（临时把实现改回 `idx = 0` 退化）必须让本条变红。
func TestCursorKeyRejectsUnknownResolution(t *testing.T) {
	registered := uint8(len(cursorKeySuffixes))
	for _, unknown := range []Resolution{Resolution(registered), Resolution(registered + 1), Resolution(255)} {
		key, err := CursorKey(1001, unknown)
		if err == nil {
			t.Fatalf("CursorKey(1001, %d) 未报错，返回 %q —— 未登记档位必须报错，绝不退化成 5m", uint8(unknown), key)
		}
		if key != "" {
			t.Fatalf("CursorKey(1001, %d) 报错时仍返回键名 %q（必须返回空串：半个键名会被真的拿去读写 Redis）",
				uint8(unknown), key)
		}
		// 退化的具体形态就是「等于 5m 的键」：显式钉住这一条，避免将来有人用
		// 「返回 nil error + 5m 键」的方式把它悄悄放回来。
		if key5m, _ := CursorKey(1001, Resolution5m); key == key5m {
			t.Fatalf("未登记档位 %d 的键退化成 5m 的键 %q（两个档位会互相覆盖水位）", uint8(unknown), key5m)
		}
	}
	// 受控枚举的两个常量必须照常工作（这条断言防止「一律报错」式的过度修复）。
	if _, err := CursorKey(1001, Resolution5m); err != nil {
		t.Fatalf("已登记档位 Resolution5m 报错: %v", err)
	}
	if _, err := CursorKey(1001, Resolution1h); err != nil {
		t.Fatalf("已登记档位 Resolution1h 报错: %v", err)
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
