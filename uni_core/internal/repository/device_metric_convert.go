package repository

import (
	"encoding/json"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agentmetrics"
)

// Wide 与 entity.DeviceMetricWide 之间用 **JSON 往返**转换。
//
// 为什么不用逐字段赋值：宽表有 40+ 列，逐字段双向映射是 ~86 行机械代码，
// 且每加一列就要改两处 —— 那是另一个漂移面。
//
// 为什么 JSON 往返是安全的：两个结构体的字段名（snake_case 后的 column 名）
// 与类型**必须逐一对应**，这一点由 device_metric_convert_test.go 的
// TestWideAndEntityFieldsMirror 守卫；一旦漂移，守卫立刻失败，
// 而不是等运行时静默丢字段。
//
// 性能：flush 路径每 5min 每设备每桶转换一次（不是热循环），
// 40 字段的 JSON 往返在微秒量级，可忽略。
func EntityFromWide(w agentmetrics.Wide) (*entity.DeviceMetricWide, error) {
	b, err := json.Marshal(w)
	if err != nil {
		return nil, err
	}
	var e entity.DeviceMetricWide
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, err
	}
	e.DeviceID = w.DeviceID
	e.BucketTS = w.BucketTS
	return &e, nil
}

func WideFromEntity(e *entity.DeviceMetricWide) (agentmetrics.Wide, error) {
	if e == nil {
		return agentmetrics.Wide{}, nil
	}
	b, err := json.Marshal(e)
	if err != nil {
		return agentmetrics.Wide{}, err
	}
	var w agentmetrics.Wide
	if err := json.Unmarshal(b, &w); err != nil {
		return agentmetrics.Wide{}, err
	}
	w.DeviceID = e.DeviceID
	w.BucketTS = e.BucketTS
	return w, nil
}
