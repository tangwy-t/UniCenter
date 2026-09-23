package agentproto

import (
	"errors"
	"strconv"
	"strings"
)

// Semver 是语义化版本号。
//
// 不含 build 元数据（`1.2.3+sha.abc`）：它既不参与比较，也没有展示价值，
// 解析时直接丢弃。
type Semver struct {
	Major, Minor, Patch int
	// Pre 是预发布标识（`1.2.3-rc.1` 里的 `rc.1`），空串表示正式版。
	Pre string
}

// ErrInvalidSemver 表示版本号不是合法形态。
var ErrInvalidSemver = errors.New("agentproto: invalid semver")

func (v Semver) String() string {
	out := strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor) + "." + strconv.Itoa(v.Patch)
	if v.Pre != "" {
		out += "-" + v.Pre
	}
	return out
}

// ParseSemver 解析 `major.minor.patch[-pre][+build]`。
//
// 刻意比语义化版本规范更严：拒绝缺段的 `1.2`、前导零的 `01`、空预发布标识的
// `1.2.3-`、带空白的 `1.2.3 `。理由：这个解析器守的是「升级目标」这个会写进协议、
// 落进数据库、决定设备是否替换二进制的字段。宽松解析会把拼写错误变成版本号，
// 而两端各自的比较结果还可能不一致 —— 最终表现为「怎么点升级都升不上去」，
// 而日志里一切正常。**宁可让拼写错误在写入时就被拒绝。**
//
// 环境里存在非 semver 的自述版本（dev 构建上报 `dev`），故调用方要允许解析失败：
// 「不是 semver」是合法状态，不是错误 —— 它只是永远不满足任何目标版本。
func ParseSemver(s string) (Semver, error) {
	var v Semver
	if s == "" || strings.TrimSpace(s) != s {
		return v, ErrInvalidSemver
	}
	// 丢弃 build 元数据（第一个 `+` 之后）。空的 build 段（`1.2.3+`）是拼写错误，
	// 与「没有 build 元数据」不是一回事，必须拒绝而不是当成前者。
	if i := strings.IndexByte(s, '+'); i >= 0 {
		build := s[i+1:]
		if build == "" || !validPreIdent(build) {
			return Semver{}, ErrInvalidSemver
		}
		s = s[:i]
	}
	// 预发布标识（第一个 `-` 之后）。
	if i := strings.IndexByte(s, '-'); i >= 0 {
		v.Pre = s[i+1:]
		s = s[:i]
		if v.Pre == "" || !validPreIdent(v.Pre) {
			return Semver{}, ErrInvalidSemver
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Semver{}, ErrInvalidSemver
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := parseNumericIdent(p)
		if err != nil {
			return Semver{}, ErrInvalidSemver
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, nil
}

// IsSemver 报告 s 是否是合法版本号形态。
func IsSemver(s string) bool {
	_, err := ParseSemver(s)
	return err == nil
}

// CompareSemver 按语义化版本规范比较：返回 -1 / 0 / 1。
//
// 预发布规则：`1.2.3-rc.1` < `1.2.3`（有预发布标识的一律小于同号正式版）——
// 这条不能省：升级目标写 `0.2.0` 时，设备上的 `0.2.0-rc.1` 必须被判为「不同」
// 并升上去，否则「先发 rc 再发正式版」这条路走不通。
func CompareSemver(a, b Semver) int {
	for _, c := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if c[0] != c[1] {
			if c[0] < c[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.Pre == "" && b.Pre == "":
		return 0
	case a.Pre == "":
		return 1 // 正式版 > 预发布版
	case b.Pre == "":
		return -1
	}
	return comparePre(a.Pre, b.Pre)
}

// comparePre 比较两个预发布标识（两者都非空）。
func comparePre(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aErr := parseNumericIdent(as[i])
		bn, bErr := parseNumericIdent(bs[i])
		switch {
		case aErr == nil && bErr == nil:
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
		case aErr == nil: // 纯数字标识 < 字母数字标识
			return -1
		case bErr == nil:
			return 1
		default:
			if as[i] != bs[i] {
				if as[i] < bs[i] {
					return -1
				}
				return 1
			}
		}
	}
	switch {
	case len(as) == len(bs):
		return 0
	case len(as) < len(bs): // 前缀相同则标识少的小
		return -1
	default:
		return 1
	}
}

// parseNumericIdent 解析一个数字标识，并拒绝前导零（`01`）。
func parseNumericIdent(s string) (int, error) {
	if s == "" {
		return 0, ErrInvalidSemver
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, ErrInvalidSemver
		}
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, ErrInvalidSemver
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, ErrInvalidSemver
	}
	return n, nil
}

// validPreIdent 校验预发布标识的字符集（[0-9A-Za-z-]，以 `.` 分段，段不可为空）。
func validPreIdent(s string) bool {
	for _, seg := range strings.Split(s, ".") {
		if seg == "" {
			return false
		}
		for i := 0; i < len(seg); i++ {
			c := seg[i]
			switch {
			case c >= '0' && c <= '9':
			case c >= 'a' && c <= 'z':
			case c >= 'A' && c <= 'Z':
			case c == '-':
			default:
				return false
			}
		}
	}
	return true
}
