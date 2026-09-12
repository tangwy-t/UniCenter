package passwordpolicy

import "testing"

func TestValidate_Length(t *testing.T) {
	p := Policy{MinLength: 8, MinCategories: 3, ForbidContainingUsername: true}
	if err := p.Validate("Ab1!xxxx", "alice"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
	if err := p.Validate("Ab1!", "alice"); err == nil {
		t.Fatal("short password accepted")
	}
}

func TestValidate_Categories(t *testing.T) {
	p := Policy{MinLength: 8, MinCategories: 3, ForbidContainingUsername: true}
	if err := p.Validate("abcdefgh", "alice"); err == nil {
		t.Fatal("lowercase-only accepted")
	}
	if err := p.Validate("AbCDEFGH", "alice"); err == nil {
		t.Fatal("upper+lower only accepted")
	}
}

func TestValidate_ForbidUsername(t *testing.T) {
	p := Policy{MinLength: 8, MinCategories: 0, ForbidContainingUsername: true}
	if err := p.Validate("Alice123!", "alice"); err == nil {
		t.Fatal("password containing username accepted")
	}
}

func TestCategoryCount(t *testing.T) {
	if got := categoryCount("aA1!"); got != 4 {
		t.Fatalf("categoryCount(aA1!) = %d, want 4", got)
	}
	if got := categoryCount("中文!"); got != 1 {
		t.Fatalf("categoryCount(中文!) = %d, want 1 (non-ASCII counts as special)", got)
	}
}
