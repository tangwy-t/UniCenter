package agentmetrics

import "fmt"

// Resolution 标识指标档位，也是**游标族与表选择的单一维度**。
//
// 为什么把它做成类型而不是 int/字符串：flush 只写 5m、rollup 只写 1h（Plan 2A 的
// 「两段提交」），两者消费**同一族游标**（`cursor_5m` / `cursor_1h`）却互不阻塞。
// 档位一旦散成裸数字，就会在 service 里出现 `%d` 手拼键名与 `+3600` 之类的
// 魔法常量 —— 那是「键名契约」与「档位语义」两个漂移面。
type Resolution uint8

const (
	// Resolution5m 是 5min 档（表 device_metric_5m，bucket 宽 300s）。
	Resolution5m Resolution = iota
	// Resolution1h 是 1h 档（表 device_metric_1h，bucket 宽 3600s）。
	Resolution1h
)

// BucketSeconds 返回该档位的桶宽（秒）。
func (r Resolution) BucketSeconds() int64 {
	if r == Resolution1h {
		return 3600
	}
	return 300
}

// String 让档位在日志/错误里可读（不做 i18n，纯标识）。
func (r Resolution) String() string {
	if r == Resolution1h {
		return "1h"
	}
	return "5m"
}

// cursorKeySuffixes 是游标键的**唯一**后缀清单。
//
// 不用 String() 拼后缀：String() 是给人看的显示名（未来可能改成 "5min"/"1 hour"），
// 而后缀是 **Redis key 契约**，必须与 spec §7 表格逐字一致（`cursor_5m`/`cursor_1h`）。
// 两者共用一个变量会让「改显示名」顺手改坏键名契约。
var cursorKeySuffixes = [...]string{"cursor_5m", "cursor_1h"}

// CursorKey 返回该设备在该档位的水位键。
//
// 键名契约（与 spec §7 表格逐字一致，也是 flush/rollup/运维手工 DEL 的共用契约）：
//
//	agent:device:{id}:cursor_5m
//	agent:device:{id}:cursor_1h
//
// 形态与 latestKey/historyKey **同源**（都是 `agent:device:{id}:xxx`，前缀复用
// historyKeyPrefix）。曾经把 latest 写成 `agent:device:latest:{id}`（档位/名字在前、
// 设备号在后）—— 这类偏差不会让读写自洽的测试变红，却会让**别的**消费方按 spec 的
// 键名找不到数据，且症状是「静默读不到」，见 raw.go 里 latestKey 的注释。
// 契约守卫见 resolution_test.go 的 TestCursorKeyMatchesSpecContract（用字符串字面量
// 钉住键名，不复用本函数）。
func CursorKey(deviceID uint64, r Resolution) string {
	idx := int(r)
	if idx < 0 || idx >= len(cursorKeySuffixes) {
		// 未知档位绝不静默退化成 5m：那会让 rollup 的水位写进 flush 的键里，
		// 两个档位互相覆盖水位（症状是「某些桶永远重算 / 永远漏算」）。
		idx = 0
	}
	return fmt.Sprintf("%s%d:%s", historyKeyPrefix, deviceID, cursorKeySuffixes[idx])
}

// repairKeySuffix 是 1h 回滚的 repair 集合键后缀。
//
// 与 cursorKeySuffixes **分开**登记：它们是两族键（游标是水位、repair 是待重算队列），
// 合并成一张表会让「给游标加档位」顺手改出 `repair_5m` 这种没有语义的键名。
const repairKeySuffix = "repair_1h"

// RepairSetKey 返回该设备的 1h repair 集合键。
//
// 集合成员是**需要重算的小时桶起点**（unix 秒的十进制字符串）——成员本身就是小时，
// 故「最老的那个」= 最小的成员，不需要额外维护「首次发现时间」这类元数据
// （元数据多一份就多一个漂移面，且它一旦丢失，集合就无法按年龄收敛）。
// 成员全部移除后 Redis 会自动删掉空集合，不留空键。
//
// 为什么只有 1h 变体、没有 Resolution 参数：repair 的语义是「**可推导**数据与真值不一致，
// 需要重算」，而两者当中只有 1h 是推导出来的（5m 是唯一真值来源）。给它配一个
// `repair_5m` 的对称键，只会把「5m 也可重算」这个错误观念变得顺手。
//
// 形态与 CursorKey **同源**（`agent:device:{id}:xxx`，前缀复用 historyKeyPrefix）：
//
//	agent:device:{id}:repair_1h
//
// 键名契约由 agent_metrics_rollup_test.go 用字符串字面量钉住（不复用本函数）。
func RepairSetKey(deviceID uint64) string {
	return fmt.Sprintf("%s%d:%s", historyKeyPrefix, deviceID, repairKeySuffix)
}
