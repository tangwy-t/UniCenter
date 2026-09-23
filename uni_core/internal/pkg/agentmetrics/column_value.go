package agentmetrics

// 本文件提供 TrendPoint 的**列名 → 值**正向读取，供总览页把「一行 24 列」
// 转置成「一列 N 个值」的列式形状使用。
//
// ── 为什么需要第三份列映射，以及为什么不担心三份漂移 ──────────────────
//
// 本包内已有两份列清单：
//   - trendPointColumns（trend_columns.go）：声明「TrendPoint 承接哪些列」；
//   - mergeAcc.add 里的 []struct{key, v}（merge.go）：把列名绑到字段做归并聚合。
//
// 现在加第三份「列名 → 字段」看起来是在制造漂移风险，但它其实是**第三份的
// 唯一正确形态**：前两份各自锁死了一半事实（有哪些列 / 如何聚合），而消费方
// 需要的恰是二者的交集 —— 「按列名取值」。不在这里写，这个映射就会以
// switch 语句的形式散落到 service 层，届时它会**脱离**本包的守卫网络
// （列清单的镜像测试在 agentmetrics 包内），漂移将无人发现。
//
// 漂移由测试 TestColumnValueCoversTrendPointColumns 守卫：遍历
// TrendPointColumns()，逐列断言本函数返回值与「反射读该字段」一致。
// 新增列却忘了在本函数登记时该测试会红灯，且错误信息直接点出列名。

// ColumnValue 取 TrendPoint 上指定列的当前值。
//
// 返回 (*float64, true) 表示该列被本类型承接（值本身仍可能为 nil —— 那表示
// **该桶未采集到**，与「该列不被承接」是两件事）；
// 返回 (nil, false) 表示列名不在本类型承接范围内。
//
// 调用方**必须**区分这两种情况：前者在图上是一个空洞，后者说明请求了一个
// 本档位根本不产的列（应当报 400，而不是画一条恒空的线）。
//
// 计数字段（tcp_*/proc_count）在此统一转成 float64：总览页把它们当普通数值
// 列处理（统计量、图表都是数值轴），在响应里再窄化回整数没有收益。
func ColumnValue(p TrendPoint, column string) (*float64, bool) {
	switch column {
	case "cpu_used_percent":
		return p.CPUUsedPercent, true
	case "cpu_iowait":
		return p.CPUIOWait, true
	case "load1":
		return p.Load1, true
	case "load5":
		return p.Load5, true
	case "load15":
		return p.Load15, true
	case "mem_used_percent":
		return p.MemUsedPercent, true
	case "mem_used_mb":
		return p.MemUsedMB, true
	case "mem_available_mb":
		return p.MemAvailableMB, true
	case "swap_used_percent":
		return p.SwapUsedPercent, true
	case "swap_used_mb":
		return p.SwapUsedMB, true
	case "disk_total_gb":
		return p.DiskTotalGB, true
	case "disk_used_gb":
		return p.DiskUsedGB, true
	case "disk_used_percent":
		return p.DiskUsedPercent, true
	case "disk_io_read_bytes_sec":
		return p.DiskIOReadBytesSec, true
	case "disk_io_write_bytes_sec":
		return p.DiskIOWriteBytesSec, true
	case "nic_rx_bytes_sec":
		return p.NICRXBytesSec, true
	case "nic_tx_bytes_sec":
		return p.NICTXBytesSec, true
	case "max_temperature_c":
		return p.MaxTemperatureC, true
	case "tcp_total":
		return intToF64Ptr(p.TCPTotal), true
	case "tcp_established":
		return intToF64Ptr(p.TCPEstablished), true
	case "tcp_listen":
		return intToF64Ptr(p.TCPListen), true
	case "proc_count":
		return intToF64Ptr(p.ProcCount), true
	case "uptime_sec":
		return intToF64Ptr(p.UptimeSec), true
	}
	return nil, false
}

// LatestToWatermark 把水位投影成「列名 → 值」的 map（键名与整机宽表列名一致）。
//
// 水位的字段集**小于** TrendPoint（只有 9 个字段，是 projectLatest 挑出来的
// 即时快照），故它不参与 ColumnValue 的镜像守卫，而是走自己的字段清单。
//
// 缺值列**不进 map**（而不是进 map 且为 nil）：消费方用「键是否存在」判断
// 「有没有采到」，空 map 与「全 0 的 map」因此在形状上就不可混淆 —— 这与
// 响应 DTO 的 omitempty 决策保持一致。
func LatestToWatermark(w *LatestSummary) map[string]float64 {
	if w == nil {
		return nil
	}
	out := make(map[string]float64, 9)
	put := func(key string, v *float64) {
		if v != nil {
			out[key] = *v
		}
	}
	put("cpu_used_percent", w.CPUUsedPercent)
	put("load1", w.Load1)
	put("mem_used_percent", w.MemUsedPercent)
	put("mem_used_mb", w.MemUsedMB)
	put("disk_total_gb", w.DiskTotalGB)
	put("disk_used_gb", w.DiskUsedGB)
	put("disk_used_percent", w.DiskUsedPercent)
	put("nic_rx_bytes_sec", w.NICRXBytesSec)
	put("nic_tx_bytes_sec", w.NICTXBytesSec)
	put("max_temperature_c", w.MaxTemperatureC)
	if len(out) == 0 {
		return nil
	}
	return out
}

// intToF64Ptr 把可空的计数字段（*int64）转成可空 float64。
// nil 透传为 nil —— 「未采集」绝不变成 0。
func intToF64Ptr(v *int64) *float64 {
	if v == nil {
		return nil
	}
	f := float64(*v)
	return &f
}
