package response

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// bigID exceeds JavaScript's Number.MAX_SAFE_INTEGER (2^53-1). Every snowflake
// ID field in every response DTO must serialize as a JSON string — a bare
// number would silently lose precision once parsed by any JS client.
const bigID = uint64(9223372036854775808) // 2^63

// expectJSON marshals v and asserts the exact JSON string for the given field
// path (flat fields only; nested cases are covered by dedicated tests below).
func expectJSON(t *testing.T, v any, want string) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal(%T) error: %v", v, err)
	}
	if string(data) != want {
		t.Fatalf("Marshal(%T)\n got  %s\n want %s", v, data, want)
	}
}

func TestAllSnowflakeIDFieldsSerializeAsString(t *testing.T) {
	at := util.JSONTime{}
	expectJSON(t, IDResp{ID: bigID}, `{"id":"9223372036854775808"}`)
	expectJSON(t, LoginLogResp{ID: bigID, UserID: bigID, LoginTime: at},
		`{"id":"9223372036854775808","userId":"9223372036854775808","username":"","ip":"","location":"","browser":"","os":"","code":0,"msg":"","loginTime":"0001-01-01 00:00:00"}`)
	expectJSON(t, OperationLogResp{ID: bigID, UserID: bigID, OperTime: at},
		`{"id":"9223372036854775808","userId":"9223372036854775808","username":"","module":"","operationType":"","requestMethod":"","requestUrl":"","requestParams":"","responseResult":"","costTime":0,"ip":"","code":0,"errorMsg":"","operTime":"0001-01-01 00:00:00"}`)
	expectJSON(t, UserResp{ID: bigID, DeptID: bigID, CreatedAt: at},
		`{"id":"9223372036854775808","username":"","realName":"","nickname":"","gender":"","email":"","phone":"","avatar":"","deptId":"9223372036854775808","deptName":"","status":0,"remark":"","lastLoginTime":null,"createdAt":"0001-01-01 00:00:00","roleNames":null,"roleIds":null}`)
	expectJSON(t, RoleResp{ID: bigID, CreatedAt: at},
		fmt.Sprintf(`{"id":"9223372036854775808","name":"","code":"","dataScope":0,"sort":0,"status":0,"remark":"","menuIds":%s,"deptIds":%s,"createdAt":"0001-01-01 00:00:00"}`, "null", "null"))
	expectJSON(t, DeptResp{ID: bigID, ParentID: bigID, CreatedAt: at},
		`{"id":"9223372036854775808","parentId":"9223372036854775808","ancestors":"","name":"","sort":0,"leader":"","phone":"","email":"","status":0,"createdAt":"0001-01-01 00:00:00"}`)
	expectJSON(t, MenuResp{ID: bigID, ParentID: bigID, CreatedAt: at},
		`{"id":"9223372036854775808","parentId":"9223372036854775808","name":"","type":"","perms":"","path":"","component":"","icon":"","sort":0,"visible":0,"status":0,"createdAt":"0001-01-01 00:00:00"}`)
	expectJSON(t, DictDataResp{ID: bigID, TypeID: bigID, CreatedAt: at, UpdatedAt: at},
		`{"id":"9223372036854775808","typeId":"9223372036854775808","listClass":"","label":"","value":"","isDefault":0,"sort":0,"status":0,"remark":"","createdAt":"0001-01-01 00:00:00","updatedAt":"0001-01-01 00:00:00"}`)
	expectJSON(t, DictTypeResp{ID: bigID, CreatedAt: at, UpdatedAt: at},
		`{"id":"9223372036854775808","code":"","name":"","status":0,"remark":"","createdAt":"0001-01-01 00:00:00","updatedAt":"0001-01-01 00:00:00"}`)
	expectJSON(t, ConfigResp{ID: bigID, CreatedAt: at, UpdatedAt: at},
		`{"id":"9223372036854775808","name":"","configKey":"","configValue":"","configType":"","remark":"","status":0,"createdAt":"0001-01-01 00:00:00","updatedAt":"0001-01-01 00:00:00"}`)
	expectJSON(t, NoticeResp{ID: bigID, CreatedAt: at, UpdatedAt: at},
		`{"id":"9223372036854775808","title":"","content":"","noticeType":0,"status":0,"priority":0,"publishType":0,"targetType":0,"targetIds":"","targetDesc":"","createBy":"","publishTime":null,"createdAt":"0001-01-01 00:00:00","updatedAt":"0001-01-01 00:00:00"}`)
	expectJSON(t, UserInfoResp{ID: bigID, DeptID: bigID},
		`{"id":"9223372036854775808","username":"","realName":"","nickname":"","gender":"","avatar":"","email":"","phone":"","dataScope":0,"deptId":"9223372036854775808","roles":null,"permissions":null,"mustChangePassword":false}`)
	expectJSON(t, JobResp{ID: bigID, CreatedAt: at, UpdatedAt: at},
		`{"id":"9223372036854775808","name":"","jobGroup":"","cronExpression":"","invokeTarget":"","invokeParams":"","concurrent":0,"retryCount":0,"retryInterval":0,"status":0,"runAtStartup":0,"remark":"","nextRunTime":null,"createdAt":"0001-01-01 00:00:00","updatedAt":"0001-01-01 00:00:00"}`)
	expectJSON(t, JobLogResp{ID: bigID, JobID: bigID, StartTime: at},
		`{"id":"9223372036854775808","jobId":"9223372036854775808","jobName":"","jobGroup":"","invokeTarget":"","triggerType":0,"startTime":"0001-01-01 00:00:00","endTime":null,"costTime":0,"status":0,"errorMsg":""}`)
	expectJSON(t, ListKeysResponse{Cursor: bigID},
		`{"keys":null,"cursor":"9223372036854775808"}`)
}

func TestRoleRespMenuDeptIDsSerializeAsStringArray(t *testing.T) {
	at := util.JSONTime{}
	resp := RoleResp{
		ID:        bigID,
		MenuIDs:   util.JsonUint64Slice{bigID, 1},
		DeptIDs:   util.JsonUint64Slice{bigID},
		CreatedAt: at,
	}
	expectJSON(t, resp,
		`{"id":"9223372036854775808","name":"","code":"","dataScope":0,"sort":0,"status":0,"remark":"","menuIds":["9223372036854775808","1"],"deptIds":["9223372036854775808"],"createdAt":"0001-01-01 00:00:00"}`)
}

func TestTreeResponsesNestStringIDs(t *testing.T) {
	// Nested children (menu/dept trees) must keep string IDs at every level.
	at := util.JSONTime{}
	tree := []DeptResp{{
		ID: bigID, ParentID: 0, CreatedAt: at,
		Children: []DeptResp{{ID: bigID - 1, ParentID: bigID, CreatedAt: at}},
	}}
	data, err := json.Marshal(tree)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	want := `[{"id":"9223372036854775808","parentId":"0","ancestors":"","name":"","sort":0,"leader":"","phone":"","email":"","status":0,"children":[{"id":"9223372036854775807","parentId":"9223372036854775808","ancestors":"","name":"","sort":0,"leader":"","phone":"","email":"","status":0,"createdAt":"0001-01-01 00:00:00"}],"createdAt":"0001-01-01 00:00:00"}]`
	if string(data) != want {
		t.Fatalf("Marshal(tree)\n got  %s\n want %s", data, want)
	}
}
