package agentproto

import (
	"encoding/json"
	"reflect"
	"strings"
)

// FieldShape 描述一个 DTO 字段在线上契约里的形状。
type FieldShape struct {
	Name      string `json:"name"`
	JSON      string `json:"json"`
	Type      string `json:"type"`
	Pointer   bool   `json:"pointer"`
	OmitEmpty bool   `json:"omit_empty"`
}

// snapshottedDTOs 列出参与形状快照的全部载荷类型。
// 新增 DTO 时必须在此登记，否则形状漂移不可见。
func snapshottedDTOs() []struct {
	name string
	v    any
} {
	return []struct {
		name string
		v    any
	}{
		{"Hello", Hello{}},
		{"HelloAck", HelloAck{}},
		{"MetricsSample", MetricsSample{}},
		{"DiskMetric", DiskMetric{}},
		{"DiskIOMetric", DiskIOMetric{}},
		{"NICMetric", NICMetric{}},
		{"SensorMetric", SensorMetric{}},
		{"AgentMetric", AgentMetric{}},
	}
}

// Snapshot 返回「DTO 名 → 字段形状清单」，字段顺序与 struct 声明顺序一致。
func Snapshot() map[string][]FieldShape {
	out := map[string][]FieldShape{}
	for _, dto := range snapshottedDTOs() {
		t := reflect.TypeOf(dto.v)
		fields := make([]FieldShape, 0, t.NumField())
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			name, opts := splitJSONTag(tag)
			fields = append(fields, FieldShape{
				Name:      f.Name,
				JSON:      name,
				Type:      f.Type.String(),
				Pointer:   f.Type.Kind() == reflect.Ptr,
				OmitEmpty: opts["omitempty"],
			})
		}
		out[dto.name] = fields
	}
	return out
}

// SnapshotJSON 稳定序列化形状快照，供 CI 做 regenerate-and-diff。
func SnapshotJSON() ([]byte, error) {
	return json.MarshalIndent(Snapshot(), "", "  ")
}

func splitJSONTag(tag string) (string, map[string]bool) {
	opts := map[string]bool{}
	if tag == "" {
		return "", opts
	}
	parts := strings.Split(tag, ",")
	for _, o := range parts[1:] {
		opts[o] = true
	}
	return parts[0], opts
}
