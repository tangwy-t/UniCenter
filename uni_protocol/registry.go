package agentproto

// TypeSpec 描述一个消息类型的静态属性，是解码分发的唯一来源。
type TypeSpec struct {
	Type      string
	Direction Direction
	Kind      Kind
	New       func() any
}

// typeRegistry 是消息类型的唯一枚举源。
//
// 新增一个消息类型时必须同时：① 在 types.go 加常量；② 在此登记（含方向、载荷种类与工厂）。
// guard_test.go 会双向比对，漏一步即测试失败。
var typeRegistry = []TypeSpec{
	{Type: TypeAgentHello, Direction: DirAgentToCore, Kind: KindPayload, New: func() any { return &Hello{} }},
	{Type: TypeCoreHelloAck, Direction: DirCoreToAgent, Kind: KindPayload, New: func() any { return &HelloAck{} }},
	{Type: TypeAgentHeartbeat, Direction: DirAgentToCore, Kind: KindEmpty, New: func() any { return &Heartbeat{} }},
	{Type: TypeAgentReportMetrics, Direction: DirAgentToCore, Kind: KindPayload, New: func() any { return &MetricsSample{} }},
}

// AllTypes 返回按注册顺序排列的全部消息类型。
func AllTypes() []string {
	out := make([]string, 0, len(typeRegistry))
	for _, spec := range typeRegistry {
		out = append(out, spec.Type)
	}
	return out
}

// LookupType 按 type 串查找类型规格。
func LookupType(t string) (TypeSpec, bool) {
	for _, spec := range typeRegistry {
		if spec.Type == t {
			return spec, true
		}
	}
	return TypeSpec{}, false
}

// IsKnownType 报告 type 串是否已登记。
func IsKnownType(t string) bool {
	_, ok := LookupType(t)
	return ok
}

// DirectionOf 返回 type 串的允许方向；未登记时返回 DirUnknown。
func DirectionOf(t string) Direction {
	spec, ok := LookupType(t)
	if !ok {
		return DirUnknown
	}
	return spec.Direction
}

// DecodeTyped 按信封的 type 把 Data 解码为具体载荷类型。
// 未知类型返回 ErrUnknownType（调用方应「忽略并计数」，而不是断开连接）。
func DecodeTyped(m *Message) (any, error) {
	spec, ok := LookupType(m.Type)
	if !ok {
		return nil, decodeErr(StageType, "type", ErrUnknownType)
	}
	return decodeSpecPayload(spec, m)
}

// DecodeTypedFor 在解码前额外校验本端允许的方向，用于拒绝回环/伪造消息。
// 方向不符时返回 ErrUnknownType（对外表现为「我不认识这个类型」）。
func DecodeTypedFor(m *Message, allowed Direction) (any, error) {
	spec, ok := LookupType(m.Type)
	if !ok || spec.Direction != allowed {
		return nil, decodeErr(StageType, "type", ErrUnknownType)
	}
	return decodeSpecPayload(spec, m)
}

func decodeSpecPayload(spec TypeSpec, m *Message) (any, error) {
	out := spec.New()
	if spec.Kind == KindEmpty {
		return out, nil
	}
	if err := m.DecodeData(out); err != nil {
		return nil, err
	}
	return out, nil
}
