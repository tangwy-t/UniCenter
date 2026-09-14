package agentproto

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// 本文件补齐 spec §5.8 要求的「单源 + 守卫」覆盖面：
// 版本、关闭码、DTO 名单各自的双向守卫，以及形状漂移的 additive-only 守卫。

// ---------- 单源双向守卫：版本 ----------

func TestVersionsAreConsistent(t *testing.T) {
	vs := SupportedVersions()
	if len(vs) == 0 {
		t.Fatal("SupportedVersions() 为空")
	}
	seen := map[int]bool{}
	for i, v := range vs {
		if i > 0 && v <= vs[i-1] {
			t.Fatalf("SupportedVersions() 未严格递增: %v", vs)
		}
		if seen[v] {
			t.Fatalf("SupportedVersions() 含重复: %v", vs)
		}
		seen[v] = true
	}
	if !seen[MinVersion] || !seen[CurrentVersion] {
		t.Fatalf("SupportedVersions() 必须含 MinVersion(%d) 与 CurrentVersion(%d): %v", MinVersion, CurrentVersion, vs)
	}
	if CurrentVersion != vs[len(vs)-1] {
		t.Fatalf("CurrentVersion(%d) 必须是最大支持版本(%d)", CurrentVersion, vs[len(vs)-1])
	}
	if !IsSupported(MinVersion) || IsSupported(CurrentVersion+1) {
		t.Fatal("IsSupported 与 SupportedVersions 不一致")
	}
}

// ---------- 单源双向守卫：关闭码 ----------

func TestCloseCodesAreExhaustivelyMapped(t *testing.T) {
	codes := AllCloseCodes()
	if len(codes) != len(closeReasons) {
		t.Fatalf("AllCloseCodes() 数量 %d 与 closeReasons %d 不一致（有死项或漏项）", len(codes), len(closeReasons))
	}
	// 反向：每个有 reason 的关闭码都必须出现在枚举里
	for code := range closeReasons {
		found := false
		for _, c := range codes {
			if c == code {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("关闭码 %d 有 reason 但不在 AllCloseCodes() 里", code)
		}
	}
	// 正向：枚举里每一项都必须有 reason（不得是死项）
	for _, c := range codes {
		if _, ok := closeReasons[c]; !ok {
			t.Fatalf("AllCloseCodes() 里的 %d 没有 reason 映射（死项）", c)
		}
	}
}

// ---------- 单源双向守卫：DTO 名单 ----------

// typeNameOf 把注册表里的载荷实例映射到形状快照使用的 DTO 名。
func typeNameOf(v any) string {
	switch v.(type) {
	case *Hello:
		return "Hello"
	case *HelloAck:
		return "HelloAck"
	case *MetricsSample:
		return "MetricsSample"
	default:
		return ""
	}
}

func TestSnapshotDTORegistryCoversAllPayloads(t *testing.T) {
	snap := Snapshot()
	for _, ty := range AllTypes() {
		spec, ok := LookupType(ty)
		if !ok {
			t.Fatalf("AllTypes() 里的 %q 无法 LookupType", ty)
		}
		if spec.Kind != KindPayload {
			continue
		}
		name := typeNameOf(spec.New())
		if name == "" {
			t.Fatalf("类型 %s 的载荷未在 typeNameOf 登记（新增载荷必须同步登记）", ty)
		}
		if _, ok := snap[name]; !ok {
			t.Fatalf("类型 %s 的载荷 %s 未纳入形状快照（新增 DTO 必须显式登记）", ty, name)
		}
	}
}

// ---------- 形状漂移 additive-only 守卫 ----------

const fieldSnapshotPath = "testdata/contract/baseline.v1.fieldsnapshot.json"

// TestShapeDriftAdditiveOnly 锁死「协议只能新增可选字段」这条演进铁律：
//   - 删除 / 改名 / 改类型 / 去掉 omitempty / 新增必填字段 → 破坏性变更，必须升协议版本并新建基线目录；
//   - 新增带 omitempty 的字段 → 允许（非破坏性）。
func TestShapeDriftAdditiveOnly(t *testing.T) {
	raw, err := os.ReadFile(fieldSnapshotPath)
	if err != nil {
		t.Fatalf("读取形状基线失败（基线必须随仓库提交）: %v", err)
	}
	var baseline map[string][]FieldShape
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatalf("基线不是合法 JSON: %v", err)
	}
	if len(baseline) == 0 {
		t.Fatal("基线为空")
	}
	current := Snapshot()

	// 正向：基线的每个字段都必须还在，且形状未收紧
	for dto, baseFields := range baseline {
		curFields, ok := current[dto]
		if !ok {
			t.Fatalf("基线里的 DTO %q 在当前快照中消失（破坏性变更）", dto)
		}
		curByName := map[string]FieldShape{}
		for _, f := range curFields {
			curByName[f.Name] = f
		}
		for _, bf := range baseFields {
			cf, ok := curByName[bf.Name]
			if !ok {
				t.Fatalf("%s.%s 被删除或改名（破坏性变更）", dto, bf.Name)
			}
			if cf.Type != bf.Type {
				t.Fatalf("%s.%s 类型从 %s 变为 %s（破坏性变更）", dto, bf.Name, bf.Type, cf.Type)
			}
			if cf.JSON != bf.JSON {
				t.Fatalf("%s.%s 的 json tag 从 %q 变为 %q（破坏性变更）", dto, bf.Name, bf.JSON, cf.JSON)
			}
			if bf.OmitEmpty && !cf.OmitEmpty {
				t.Fatalf("%s.%s 丢掉了 omitempty（可选字段变必填，破坏性变更）", dto, bf.Name)
			}
		}
	}

	// 反向：新增 DTO 或新增必填字段都要显式升版本
	for dto, curFields := range current {
		baseFields, existed := baseline[dto]
		if !existed {
			t.Fatalf("新增 DTO %q 必须同步更新基线 %s", dto, fieldSnapshotPath)
		}
		baseByName := map[string]bool{}
		for _, bf := range baseFields {
			baseByName[bf.Name] = true
		}
		for _, cf := range curFields {
			if baseByName[cf.Name] {
				continue
			}
			if !cf.OmitEmpty {
				t.Fatalf("%s.%s 是新增的必填字段（破坏性变更，必须升协议版本）", dto, cf.Name)
			}
		}
	}
}

// ---------- 解码路径的语义校验与方向区分 ----------

func TestIDMustBeDecimalString(t *testing.T) {
	for _, bad := range []string{"abc", "1a", "-1", "1.5", "0x1f", " 1", "١٢٣", "1_000"} {
		raw := []byte(`{"v":1,"id":"` + bad + `","type":"agent.heartbeat","ts":1,"data":{}}`)
		m, err := Decode(raw)
		if err == nil {
			t.Fatalf("非十进制 id %q 应被拒，实际通过: %+v", bad, m)
		}
		if !errors.Is(err, ErrInvalidID) {
			t.Fatalf("非十进制 id %q 应报 ErrInvalidID，实际 %v", bad, err)
		}
	}
	if _, err := Decode([]byte(`{"v":1,"id":"1700000000001","type":"agent.heartbeat","ts":1,"data":{}}`)); err != nil {
		t.Fatalf("十进制 id 应通过: %v", err)
	}
}

func TestWrongDirectionIsDistinguishableFromUnknownType(t *testing.T) {
	ack := &HelloAck{Accepted: true, DeviceID: "1", ReportInterval: 10}
	m, err := NewMessage("1", TypeCoreHelloAck, ack)
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecodeTypedFor(m, DirAgentToCore)
	if !errors.Is(err, ErrWrongDirection) {
		t.Fatalf("方向不符应报 ErrWrongDirection（供调用方映射 CloseUnsupportedType 4003），实际 %v", err)
	}
	if errors.Is(err, ErrUnknownType) {
		t.Fatal("方向不符不得被归类为 ErrUnknownType，否则「回环/伪造」与「未知类型」无法区分")
	}

	um := &Message{V: CurrentVersion, ID: "1", Type: "agent.future.type", TS: 1, Data: json.RawMessage(`{}`)}
	if _, err := DecodeTyped(um); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("未知类型必须报 ErrUnknownType（调用方应忽略并计数、不断连），实际 %v", err)
	}
}

func TestDecodeTypedValidatesPayloadSemantics(t *testing.T) {
	cases := []struct {
		name    string
		typ     string
		payload string
		want    error
	}{
		{"越界百分比", TypeAgentReportMetrics, `{"t":1,"cpu_used_percent":-1}`, ErrInvalidPayload},
		{"used_gb 超 total_gb", TypeAgentReportMetrics, `{"t":1,"disks":[{"mountpoint":"/","used_gb":600,"total_gb":500}]}`, ErrInvalidPayload},
		{"hello 缺必填", TypeAgentHello, `{"instance_id":"i","enroll_token":"e"}`, ErrMissingField},
		{"hello 双 token", TypeAgentHello, `{"instance_id":"i","hostname":"h","os":"linux","arch":"amd64","agent_version":"1","enroll_token":"e","agent_token":"a"}`, ErrInvalidPayload},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &Message{V: CurrentVersion, ID: "1", Type: c.typ, TS: 1, Data: json.RawMessage(c.payload)}
			got, err := DecodeTyped(m)
			if err == nil {
				t.Fatalf("语义非法的载荷必须被拒，实际通过: %+v", got)
			}
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want errors.Is(..., %v)", err, c.want)
			}
		})
	}
}

func TestHelloAckReportIntervalBoundAppliesToRejectedAck(t *testing.T) {
	bad := &HelloAck{Accepted: false, RejectReason: "nope", ReportInterval: 1}
	if err := bad.Validate(); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("report_interval 是字段级约束，accepted=false 时也应校验，实际 %v", err)
	}
}

// ---------- 编码路径模糊测试（spec §5.8 的第 4 个 fuzz 目标）----------

// FuzzEncode 断言编码路径的不变量：通过校验的信封必须可编码、可再解码，且二次编码字节稳定；
// 编码失败必须是可归类的 typed error。
func FuzzEncode(f *testing.F) {
	f.Add("1", TypeAgentHeartbeat, []byte(`{}`))
	f.Add("1234567890", TypeAgentReportMetrics, []byte(`{"t":1}`))
	f.Add("abc", TypeAgentHeartbeat, []byte(`{}`))
	f.Add("1", TypeAgentHeartbeat, []byte(`[]`))

	f.Fuzz(func(t *testing.T, id, typ string, data []byte) {
		m := &Message{V: CurrentVersion, ID: id, Type: typ, TS: 1, Data: json.RawMessage(data)}
		out, err := m.Marshal()
		if err != nil {
			var de *DecodeError
			if !errors.As(err, &de) {
				t.Fatalf("编码失败必须是 *DecodeError，实际 %T: %v", err, err)
			}
			return
		}
		m2, err := Decode(out)
		if err != nil {
			t.Fatalf("自编码结果必须可解码: %v", err)
		}
		out2, err := m2.Marshal()
		if err != nil {
			t.Fatalf("二次编码失败: %v", err)
		}
		if string(out) != string(out2) {
			t.Fatalf("二次编码不稳定:\n%s\n%s", out, out2)
		}
	})
}
