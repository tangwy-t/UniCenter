package request

import (
	"encoding/json"
	"testing"

	"github.com/gin-gonic/gin/binding"
)

// bigID exceeds JavaScript's Number.MAX_SAFE_INTEGER (2^53-1): any snowflake
// ID sent as a JSON number would already be corrupted client-side. These
// tests prove the request DTOs accept the string form losslessly.
const bigID = uint64(9223372036854775808) // 2^63

func TestCreateDeptReqParentIDFromString(t *testing.T) {
	var req CreateDeptReq
	payload := `{"name":"dept","parentId":"9223372036854775808"}`
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if req.ParentID == nil || uint64(*req.ParentID) != bigID {
		t.Fatalf("ParentID = %v, want %d", req.ParentID, bigID)
	}
}

func TestUpdateDeptReqParentIDFromNumber(t *testing.T) {
	// Backward compatibility: numeric input must keep working.
	var req UpdateDeptReq
	payload := `{"name":"dept","parentId":9223372036854775808}`
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if req.ParentID == nil || uint64(*req.ParentID) != bigID {
		t.Fatalf("ParentID = %v, want %d", req.ParentID, bigID)
	}
}

func TestCreateUserReqDeptIDAndRoleIDsFromString(t *testing.T) {
	var req CreateUserReq
	payload := `{"username":"alice","password":"secret1","deptId":"9223372036854775808","roleIds":["9223372036854775808","1"]}`
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if req.DeptID == nil || uint64(*req.DeptID) != bigID {
		t.Fatalf("DeptID = %v, want %d", req.DeptID, bigID)
	}
	if len(req.RoleIDs) != 2 || req.RoleIDs[0] != bigID || req.RoleIDs[1] != 1 {
		t.Fatalf("RoleIDs = %v, want [9223372036854775808 1]", []uint64(req.RoleIDs))
	}
}

func TestUpdateUserReqDeptIDNilWhenAbsent(t *testing.T) {
	// Field omitted: pointer must stay nil so the service layer treats the
	// dept association as "not provided".
	var req UpdateUserReq
	payload := `{"realName":"Alice"}`
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if req.DeptID != nil {
		t.Fatalf("DeptID = %v, want nil", req.DeptID)
	}
}

// TestUpdateUserReqEmptyStringsBecomeNil reproduces the 400 reported when
// editing a user: the frontend sent `"email": ""` / `"phone": ""`, which
// deserialized to pointers-to-empty-string and failed validator's email/min
// tags (omitempty only skips nil pointers).
func TestUpdateUserReqEmptyStringsBecomeNil(t *testing.T) {
	var req UpdateUserReq
	payload := `{"realName":"唐","email":"","phone":"  ","deptId":"2095868799814209536","remark":""}`
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if req.Email != nil {
		t.Fatalf("Email = %q, want nil", *req.Email)
	}
	if req.Phone != nil {
		t.Fatalf("Phone = %q, want nil", *req.Phone)
	}
	if req.DeptID == nil || uint64(*req.DeptID) != 2095868799814209536 {
		t.Fatalf("DeptID = %v, want 2095868799814209536", req.DeptID)
	}
	if req.RealName == nil || *req.RealName != "唐" {
		t.Fatalf("RealName = %v, want 唐", req.RealName)
	}
	// Pointer-to-"" for remark is fine (no validation on it) — still a string.
	if req.Remark == nil || *req.Remark != "" {
		t.Fatalf("Remark = %v, want pointer to empty string", req.Remark)
	}
}

// TestUpdateUserReqBindingAcceptsEmptyStrings asserts the full gin binding
// path (unmarshal + validation) used by PUT /api/v1/users/:id accepts the
// exact payload the frontend sends, whereas empty-string pointers failed it.
func TestUpdateUserReqBindingAcceptsEmptyStrings(t *testing.T) {
	payload := []byte(`{"realName":"唐","email":"","phone":"","deptId":"2095868799814209536","remark":""}`)
	var req UpdateUserReq
	if err := binding.JSON.BindBody(payload, &req); err != nil {
		t.Fatalf("BindBody error: %v", err)
	}
	valid := []byte(`{"realName":"唐","email":"a@b.com","phone":"13800000000","remark":""}`)
	if err := binding.JSON.BindBody(valid, &req); err != nil {
		t.Fatalf("BindBody(valid) error: %v", err)
	}
}
