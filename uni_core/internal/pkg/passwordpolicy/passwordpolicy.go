// Package passwordpolicy 提供密码强度规则的可配置校验。
// 纯函数,无 gorm/gin/apperror 依赖,便于独立单测与复用。
package passwordpolicy

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Policy 描述一组密码强度规则。
type Policy struct {
	MinLength                int
	MinCategories            int
	ForbidContainingUsername bool
}

// Validate 校验 password 是否符合策略;username 为空时跳过禁含用户名。
// 返回的 error 汇总所有未满足项(中文),供上层直接作为业务错误文案。
func (p Policy) Validate(password, username string) error {
	var problems []string
	if p.MinLength > 0 && utf8.RuneCountInString(password) < p.MinLength {
		problems = append(problems, fmt.Sprintf("密码至少 %d 位", p.MinLength))
	}
	if p.MinCategories > 0 {
		if got := categoryCount(password); got < p.MinCategories {
			problems = append(problems, fmt.Sprintf("需包含大写/小写/数字/特殊符号中的至少 %d 类", p.MinCategories))
		}
	}
	if p.ForbidContainingUsername && username != "" &&
		strings.Contains(strings.ToLower(password), strings.ToLower(username)) {
		problems = append(problems, "密码不能包含用户名")
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "；"))
}

// categoryCount 统计密码满足的字符类别数(大写/小写/数字/特殊符号)。
// 特殊符号 = 其余任何字符(含非 ASCII)。
func categoryCount(password string) int {
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, r := range password {
		switch {
		case r >= 'A' && r <= 'Z':
			hasUpper = true
		case r >= 'a' && r <= 'z':
			hasLower = true
		case r >= '0' && r <= '9':
			hasDigit = true
		default:
			hasSpecial = true
		}
	}
	n := 0
	for _, ok := range []bool{hasUpper, hasLower, hasDigit, hasSpecial} {
		if ok {
			n++
		}
	}
	return n
}
