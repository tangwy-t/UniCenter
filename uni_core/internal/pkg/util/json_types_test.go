package util

import (
	"encoding/json"
	"testing"
	"time"
)

// Snowflake IDs are 64-bit; JS Number only covers 2^53-1 exactly.
// This value overflows safe integers and is used to prove lossless round-trips.
const bigID = uint64(1) << 63 // 9223372036854775808

func TestJsonUint64UnmarshalStringAndNumber(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  uint64
	}{
		{"string value", `"9223372036854775808"`, bigID},
		{"number value", `9223372036854775808`, bigID},
		{"zero string", `"0"`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v JsonUint64
			if err := json.Unmarshal([]byte(tc.input), &v); err != nil {
				t.Fatalf("Unmarshal(%s) error: %v", tc.input, err)
			}
			if uint64(v) != tc.want {
				t.Fatalf("Unmarshal(%s) = %d, want %d", tc.input, uint64(v), tc.want)
			}
		})
	}
}

func TestJsonUint64MarshalAsFullPrecisionString(t *testing.T) {
	v := JsonUint64(bigID)
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	// Must serialize as a quoted string: a bare number would lose precision
	// once parsed by JavaScript.
	if string(data) != `"9223372036854775808"` {
		t.Fatalf("Marshal = %s, want %q", data, "9223372036854775808")
	}
}

func TestJsonUint64SliceRoundTrip(t *testing.T) {
	// Deserialize both []string and []number forms.
	var fromStrs JsonUint64Slice
	if err := json.Unmarshal([]byte(`["9223372036854775808","1"]`), &fromStrs); err != nil {
		t.Fatalf("Unmarshal([]string) error: %v", err)
	}
	if len(fromStrs) != 2 || fromStrs[0] != bigID || fromStrs[1] != 1 {
		t.Fatalf("Unmarshal([]string) = %v, want [9223372036854775808 1]", []uint64(fromStrs))
	}

	var fromNums JsonUint64Slice
	if err := json.Unmarshal([]byte(`[9223372036854775808,1]`), &fromNums); err != nil {
		t.Fatalf("Unmarshal([]number) error: %v", err)
	}
	if len(fromNums) != 2 || fromNums[0] != bigID || fromNums[1] != 1 {
		t.Fatalf("Unmarshal([]number) = %v, want [9223372036854775808 1]", []uint64(fromNums))
	}

	// Marshal must always emit strings to preserve precision for JS clients.
	data, err := json.Marshal(fromStrs)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	if string(data) != `["9223372036854775808","1"]` {
		t.Fatalf("Marshal = %s, want [\"9223372036854775808\",\"1\"]", data)
	}
}

func TestJsonUint64SliceNull(t *testing.T) {
	var s JsonUint64Slice
	if err := json.Unmarshal([]byte(`null`), &s); err != nil {
		t.Fatalf("Unmarshal(null) error: %v", err)
	}
	if s != nil {
		t.Fatalf("Unmarshal(null) = %v, want nil", []uint64(s))
	}
}

func TestJSONTimeMarshalByValueAndPointer(t *testing.T) {
	// Value form: must NOT fall back to struct encoding ({}).
	ts := JSONTime(time.Date(2025, 8, 29, 12, 30, 0, 0, time.UTC))
	data, err := json.Marshal(ts)
	if err != nil {
		t.Fatalf("Marshal(value) error: %v", err)
	}
	if string(data) != `"2025-08-29 12:30:00"` {
		t.Fatalf("Marshal(value) = %s, want \"2025-08-29 12:30:00\"", data)
	}
	// Pointer form must keep working too.
	data, err = json.Marshal(&ts)
	if err != nil {
		t.Fatalf("Marshal(pointer) error: %v", err)
	}
	if string(data) != `"2025-08-29 12:30:00"` {
		t.Fatalf("Marshal(pointer) = %s, want \"2025-08-29 12:30:00\"", data)
	}
}
