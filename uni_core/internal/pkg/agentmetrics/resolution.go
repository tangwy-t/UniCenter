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

// CursorKey 返回该设备在该档位的水位键；**未登记的档位返回错误**，绝不退化成 5m。
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
//
// 为什么未登记档位是**错误**而不是退化成 5m（这里是本轮修复的点，旧实现与旧注释相反）：
// 退化会让 rollup 的水位写进 flush 的键里（`cursor_1h` 的值落在 `cursor_5m` 上），
// 两个档位互相覆盖水位，症状是「某些桶永远重算 / 永远漏算」且**完全不可观测**。
// 返回错误的代价可接受：唯一的生产调用点是 agentmetrics.CursorStore 的三个方法
// （Read/Advance/Rewind），它们本就返回 error，且档位参数全部是受控枚举常量，
// 这条分支在生产路径上不可达 —— 它存在的意义是把「手滑传错档位」从静默数据损坏
// 变成一条立刻可见的错误。
func CursorKey(deviceID uint64, r Resolution) (string, error) {
	// Resolution 是 uint8，故不存在负值下标；越界只有「大于已登记档位数」一种。
	if int(r) >= len(cursorKeySuffixes) {
		return "", fmt.Errorf("agentmetrics: 未登记的指标档位 %d（已登记：%v）",
			uint8(r), cursorKeySuffixes)
	}
	return fmt.Sprintf("%s%d:%s", historyKeyPrefix, deviceID, cursorKeySuffixes[r]), nil
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

// rewindMarkerSuffixes 是回退「待追平」标记键的**唯一**后缀清单（与 cursorKeySuffixes 同族分列）。
//
// 为什么不与游标后缀共用一张表：它们是不同的键族（游标是水位本身，标记是「水位被退回过、
// 但还没走回来」的一次观测），合并成一张表会让「给游标加档位」顺手长出一个没有消费方的
// `rewind_<新档位>` 键。理由与 repairKeySuffix 单列时逐字相同。
var rewindMarkerSuffixes = [...]string{"rewind_5m", "rewind_1h"}

// RewindMarkerKey 返回该设备该档位的**回退「待追平」标记**键；未登记的档位返回错误，
// 绝不退化成 5m —— 与 CursorKey 同一条纪律（键名漂移的症状是「静默读不到」，
// 而这里更糟：读不到的标记看起来就是「没有待追平的回退」）。
//
// 键名契约（形态与 CursorKey/RepairSetKey 同源，前缀复用 historyKeyPrefix）：
//
//	agent:device:{id}:rewind_5m
//	agent:device:{id}:rewind_1h
//
// 值 = 该次回退写下的**目标水位**（unix 秒的十进制），语义是「水位被退回到这里、
// 随后要再走回来」。键不存在 = 该设备当前没有待追平的回退。
//
// 为什么只有 1h 那个键真的会被写：标记的读者只有一个 —— rollup 的普通区间
// （它消费 `cursor_1h`）。5m 档的回退效果由 flush 在**同一轮**的重放里体现
// （BackfillOnce 是「回退 + 重放」不可分），没有第二个读者；给 5m 也写一个标记，
// 那个键既不会被追平也不会被清除，只会永久留着一个永远为真的「待追平」
// —— 比没有标记更糟（它会教人忽略这个键）。故后缀清单两个档位都登记（键名契约完整、
// 与游标族同形），而写入侧只写 1h（见 service 层的 markRewindPending）。
func RewindMarkerKey(deviceID uint64, r Resolution) (string, error) {
	// Resolution 是 uint8，故不存在负值下标；越界只有「大于已登记档位数」一种。
	if int(r) >= len(rewindMarkerSuffixes) {
		return "", fmt.Errorf("agentmetrics: 未登记的指标档位 %d（回退标记已登记：%v）",
			uint8(r), rewindMarkerSuffixes)
	}
	return fmt.Sprintf("%s%d:%s", historyKeyPrefix, deviceID, rewindMarkerSuffixes[r]), nil
}
