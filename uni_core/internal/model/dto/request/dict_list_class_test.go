package request

import (
	"testing"

	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
)

// 复用 gin 的全局校验器（已配置 binding 标签名），验证 oneof 规则。
func testValidator(t *testing.T) *validator.Validate {
	t.Helper()
	v, ok := binding.Validator.Engine().(*validator.Validate)
	if !ok {
		t.Fatal("binding.Validator.Engine() is not *validator.Validate")
	}
	return v
}

func TestDictDataListClassValidation(t *testing.T) {
	v := testValidator(t)

	valid := []string{"primary", "success", "info", "warning", "danger"}
	for _, lc := range valid {
		req := CreateDictDataReq{Label: "x", Value: "y", ListClass: strPtr(lc)}
		if err := v.Struct(req); err != nil {
			t.Fatalf("value %q should be valid: %v", lc, err)
		}
	}

	// 非法样式值
	req := CreateDictDataReq{Label: "x", Value: "y", ListClass: strPtr("purple")}
	if err := v.Struct(req); err == nil {
		t.Fatal("listClass=purple must be rejected")
	}

	// 缺省（nil）合法
	if err := v.Struct(CreateDictDataReq{Label: "x", Value: "y"}); err != nil {
		t.Fatalf("nil listClass must be valid: %v", err)
	}
}

func strPtr(s string) *string { return &s }
