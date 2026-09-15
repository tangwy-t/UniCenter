package agentmetrics

// trendPointColumns 是 TrendPoint **实际承接**的值列名（snake_case）。
//
// 为什么需要这份清单：`available_metrics` 的设立理由（spec §8）就是消除
// 「该列未采集」与「该档位根本不产该列」的误读。若把它直接取成「该表的全部列」
// （`repository.MetricQueryColumns`，5m 档 46 列），其中约 20 列
// （`tcp_time_wait`、`tcp_close_wait`、`udp_total`、`disk_io_*_ops_sec`、
// `nic_*_packets/errors/dropped_sec`、9 个 `agent_*` …）**没有任何 TrendPoint
// 字段去装它们** —— 于是这些列恒为 nil（恒显示「—」），消费方仍然无法区分
// 「未采集」与「该档不产该列」，`available_metrics` 反而变成了误导源。
//
// 故可用列 = 该表的值列 ∩ 本清单。两份清单的漂移由
// TestTrendPointColumnsMirrorStructJSONTags 守卫（反射读 json tag，任一方向的
// 漂移都会红灯）：本清单多一列 = 声明了永远没有值的列（S3 的原缺陷），
// 少一列 = 真能承接的列被谎报为「该档无此指标」。
//
// `t`（桶起始时间）**不在**清单内：它是桶键而不是指标列，由响应契约的 `t` 承载
// （service 恒补 bucket_ts，见 D5）。`samples` 在清单内：它是可投影的真实列，
// 也是 1h 行的完整度信号（spec §8）。
var trendPointColumns = []string{
	"cpu_used_percent", "cpu_iowait", "load1", "load5", "load15",
	"mem_used_percent", "mem_used_mb", "mem_available_mb", "swap_used_percent", "swap_used_mb",
	"tcp_total", "tcp_established", "tcp_listen", "proc_count", "uptime_sec",
	"disk_total_gb", "disk_used_gb", "disk_used_percent",
	"disk_io_read_bytes_sec", "disk_io_write_bytes_sec",
	"nic_rx_bytes_sec", "nic_tx_bytes_sec", "max_temperature_c",
	"samples",
}

// TrendPointColumns 返回 TrendPoint 承接的值列名（副本；调用方不得修改内部清单）。
//
// service 用它把 `metrics=*` 与显式 `metrics` 的可用列收窄成「该表 ∩ 本清单」，
// 并据此对「表里有、响应模型装不下」的显式请求报 400（而不是静默给 nil）。
func TrendPointColumns() []string {
	out := make([]string, len(trendPointColumns))
	copy(out, trendPointColumns)
	return out
}
