package util

import (
	"encoding/json"
	"strconv"
	"time"
)

// JsonUint64 is a uint64 that serializes as a string in JSON.
// It handles both string and number values during deserialization
// for backward compatibility with frontends that send numbers.
type JsonUint64 uint64

// MarshalJSON serializes as a JSON string.
// Value receiver: works for both JsonUint64 and *JsonUint64. A pointer
// receiver here would silently fall back to number encoding for
// non-addressable values, losing precision for JS clients.
func (u JsonUint64) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strconv.FormatUint(uint64(u), 10) + `"`), nil
}

// UnmarshalJSON accepts both string and number JSON values.
func (u *JsonUint64) UnmarshalJSON(data []byte) error {
	if len(data) >= 2 && data[0] == '"' {
		s := string(data[1 : len(data)-1])
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return err
		}
		*u = JsonUint64(n)
		return nil
	}
	var n uint64
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*u = JsonUint64(n)
	return nil
}

// JsonUint64Slice is a []uint64 that serializes as []string in JSON.
type JsonUint64Slice []uint64

// MarshalJSON serializes each element as a JSON string.
// Value receiver so both JsonUint64Slice and *JsonUint64Slice marshal as
// strings; a pointer receiver would fall back to number encoding for
// non-addressable values.
func (s JsonUint64Slice) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	strs := make([]string, len(s))
	for i, v := range s {
		strs[i] = strconv.FormatUint(v, 10)
	}
	return json.Marshal(strs)
}

// UnmarshalJSON accepts both []string and []number JSON values.
func (s *JsonUint64Slice) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	var strs []string
	if err := json.Unmarshal(data, &strs); err != nil {
		var nums []uint64
		if err2 := json.Unmarshal(data, &nums); err2 != nil {
			return err
		}
		*s = JsonUint64Slice(nums)
		return nil
	}
	nums := make([]uint64, len(strs))
	for i, str := range strs {
		n, err := strconv.ParseUint(str, 10, 64)
		if err != nil {
			return err
		}
		nums[i] = n
	}
	*s = JsonUint64Slice(nums)
	return nil
}

// JSONTime is a time.Time wrapper that serializes as "2006-01-02 15:04:05" in JSON.
// It accepts both RFC 3339 and the custom format during deserialization.
// The zero value marshals as null.
type JSONTime time.Time

// MarshalJSON serializes as "2006-01-02 15:04:05".
// Value receiver: works for both JSONTime and *JSONTime. A pointer receiver
// here would silently fall back to struct encoding ({}) when the value is
// not addressable (e.g. a response struct marshaled by value).
func (t JSONTime) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Time(t).Format("2006-01-02 15:04:05") + `"`), nil
}

// UnmarshalJSON accepts RFC 3339, "2006-01-02 15:04:05", and null.
func (t *JSONTime) UnmarshalJSON(data []byte) error {
	s := string(data)
	if s == "null" || s == `""` {
		*t = JSONTime(time.Time{})
		return nil
	}
	// Strip quotes.
	if len(s) >= 2 && s[0] == '"' {
		s = s[1 : len(s)-1]
	}
	// Try our custom format first, then fall back to RFC 3339.
	parsed, err := time.Parse("2006-01-02 15:04:05", s)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, s)
		if err != nil {
			parsed, err = time.Parse("2006-01-02T15:04:05Z", s)
			if err != nil {
				return err
			}
		}
	}
	*t = JSONTime(parsed)
	return nil
}
