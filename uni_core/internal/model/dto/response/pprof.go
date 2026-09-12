package response

// PprofProfileEntry 描述 status 接口返回的单个 profile 条目。
// snapshot 类可直接采样解析（火焰图）；capture 类需按需采集后下载原始数据。
type PprofProfileEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"` // snapshot | capture
	Unit        string `json:"unit"`     // 主采样值单位（capture 类为空）
	Count       int    `json:"count"`    // snapshot 类:当前采样记录条数；capture 类:0
}

// PprofStatusResponse pprof 开关状态与 profile 概览。
type PprofStatusResponse struct {
	Enabled          bool                `json:"enabled"`
	AutoOffSeconds   int                 `json:"autoOffSeconds"`   // 0 表示不自动关闭
	RemainingSeconds int                 `json:"remainingSeconds"` // >0 时自动关闭倒计时生效
	Profiles         []PprofProfileEntry `json:"profiles"`
}

// FlameNode 火焰图树节点。Name 为缩短后的展示名（完整函数名见 Top 的 Fn）。
type FlameNode struct {
	Name     string       `json:"name"`
	Value    int64        `json:"value"`
	Children []*FlameNode `json:"children,omitempty"`
}

// PprofTopFunc 热点函数一行。
type PprofTopFunc struct {
	Fn   string `json:"fn"`   // 完整函数名
	Name string `json:"name"` // 缩短展示名
	File string `json:"file"`
	Line int    `json:"line"`
	Flat int64  `json:"flat"`
	Cum  int64  `json:"cum"`
}

// PprofProfileResponse 单个 profile 的火焰树 + 热点函数。
type PprofProfileResponse struct {
	Name        string         `json:"name"`
	Unit        string         `json:"unit"`
	SampleType  string         `json:"sampleType"`
	SampleCount int            `json:"sampleCount"`
	TotalValue  int64          `json:"totalValue"`
	Truncated   bool           `json:"truncated"` // 火焰树是否因深度/节点数限制被裁剪
	Flame       *FlameNode     `json:"flame"`
	Top         []PprofTopFunc `json:"top"`
}
