package util

import (
	"testing"

	"github.com/tangwy-t/webmanager-server/internal/pkg/logger/loggertest"
)

// Mirrors the service-layer copy pairs: request DTO (*JsonUint64) → entity (*uint64).
type copySrc struct {
	DeptID *JsonUint64
	Name   string
}

type copyDst struct {
	DeptID *uint64
	Name   string
}

// TestCopyEntityJsonUint64Pointer verifies copier's automatic conversion of
// *JsonUint64 → *uint64 (the type pair used by request DTOs → entities).
// This locks in the behavior so a copier upgrade cannot silently break it.
func TestCopyEntityJsonUint64Pointer(t *testing.T) {
	log := loggertest.New()

	t.Run("non-nil source converts to *uint64 with full precision", func(t *testing.T) {
		id := JsonUint64(9223372036854775808) // 2^63
		var dst copyDst
		// src passed as pointer, matching real service usage (req *CreateUserReq).
		CopyEntity(&dst, &copySrc{DeptID: &id, Name: "x"}, log)
		if dst.DeptID == nil || *dst.DeptID != 9223372036854775808 {
			t.Fatalf("DeptID = %v, want *9223372036854775808", dst.DeptID)
		}
		if dst.Name != "x" {
			t.Fatalf("Name = %q, want \"x\"", dst.Name)
		}
	})

	t.Run("nil source sets dest to nil", func(t *testing.T) {
		existing := uint64(7)
		dst := copyDst{DeptID: &existing}
		CopyEntity(&dst, &copySrc{Name: "y"}, log)
		if dst.DeptID != nil {
			t.Fatalf("DeptID = %v, want nil", *dst.DeptID)
		}
	})

	t.Run("non-nil source overwrites pre-existing dest", func(t *testing.T) {
		existing := uint64(7)
		id := JsonUint64(42)
		dst := copyDst{DeptID: &existing}
		CopyEntity(&dst, &copySrc{DeptID: &id}, log)
		if dst.DeptID == nil || *dst.DeptID != 42 {
			t.Fatalf("DeptID = %v, want *42", dst.DeptID)
		}
	})
}
