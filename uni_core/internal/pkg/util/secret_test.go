package util

import "testing"

func TestIsSensitiveConfigKey(t *testing.T) {
	sensitive := []string{
		"sys.jwt.secret",
		"sys.db.password",
		"sys.smtp.passwd",
		"sys.oss.private_key",
		"sys.api.credential",
	}
	for _, k := range sensitive {
		if !IsSensitiveConfigKey(k) {
			t.Errorf("%q 应被判定为敏感键", k)
		}
	}

	normal := []string{
		"sys.app.name",
		"sys.upload.maxSize",
		"sys.theme.color",
	}
	for _, k := range normal {
		if IsSensitiveConfigKey(k) {
			t.Errorf("%q 不应被判定为敏感键", k)
		}
	}
}

func TestMaskIfSensitive(t *testing.T) {
	if got := MaskIfSensitive("sys.jwt.secret", "plain"); got != MaskedValue {
		t.Errorf("敏感键应掩码为 %q, got %q", MaskedValue, got)
	}
	if got := MaskIfSensitive("sys.app.name", "webmanager"); got != "webmanager" {
		t.Errorf("非敏感键应原样返回, got %q", got)
	}
}
