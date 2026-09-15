package request

const (
	// DeviceRangeMin / DeviceRangeMax 是趋势查询允许的时间窗（秒）。
	// 1h ~ 180d（半年）。越界由 service 返回 BadRequest。
	DeviceRangeMin int64 = 3600
	DeviceRangeMax int64 = 180 * 24 * 3600

	// DeviceResourceKinds 是下钻允许的资源种类（与 device_resource.kind 一致）。
)

// DeviceMetricsQuery 是趋势 / 下钻查询参数。
//
// 设计要点：
//   - Range 用**秒**（int64）而非 Go duration 串（如 "30d"）：避免解析歧义，
//     且与响应里的 range_seconds 对称；前端直接传整秒。
//   - Kind + Name **成对出现**（有 kind 必有 name，反之亦然）：下钻时用。
//     disk 的 Name 就是挂载点（含 "/"），**不作为 URL path 段**，故不会踩转义问题。
//   - Metrics 是逗号分隔的列白名单；为空时 service 用默认列集，MetricsAll 取全量。
type DeviceMetricsQuery struct {
	Range   int64  `form:"range"`
	Kind    string `form:"kind"`
	Name    string `form:"name"`
	Metrics string `form:"metrics"`
}

// MetricsAll 是「取全量列」的哨兵值。
const MetricsAll = "*"
