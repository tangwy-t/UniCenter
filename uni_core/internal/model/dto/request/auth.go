package request

import (
	"encoding/json"
	"strings"
)

// UpdateProfileReq is the request payload for updating the current user's own
// basic profile (个人中心-基本设置). Only realName/email/phone are writable —
// roles, dept, status and other fields are explicitly out of scope here.
type UpdateProfileReq struct {
	RealName *string `json:"realName" binding:"omitempty,max=64"`
	Nickname *string `json:"nickname" binding:"omitempty,max=64"`
	Email    *string `json:"email" binding:"omitempty,email"`
	Phone    *string `json:"phone" binding:"omitempty,min=7,max=20"`
	Gender   *string `json:"gender"`
}

// UnmarshalJSON normalizes empty strings to nil before gin's binding
// validation runs (same semantics as UpdateUserReq): a JSON `"email": ""`
// means "clear the field" instead of failing the email tag with a 400.
func (u *UpdateProfileReq) UnmarshalJSON(data []byte) error {
	type alias UpdateProfileReq
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	trimToNil := func(s *string) *string {
		if s != nil && strings.TrimSpace(*s) == "" {
			return nil
		}
		return s
	}
	a.Nickname = trimToNil(a.Nickname)
	a.Gender = trimToNil(a.Gender)
	a.Email = trimToNil(a.Email)
	a.Phone = trimToNil(a.Phone)
	*u = UpdateProfileReq(a)
	return nil
}

// LoginReq is the request payload for user login.
type LoginReq struct {
	Username    string `json:"username" binding:"required"`
	Password    string `json:"password" binding:"required"`
	CaptchaKey  string `json:"captchaKey"`  // optional
	CaptchaCode string `json:"captchaCode"` // optional
}

// ChangePasswordReq is the request payload for changing a user's password.
type ChangePasswordReq struct {
	OldPassword string `json:"oldPassword" binding:"required"`
	NewPassword string `json:"newPassword" binding:"required,min=6,max=64"`
}

// RefreshTokenReq is the request payload for refreshing an access token.
type RefreshTokenReq struct {
	RefreshToken string `json:"refreshToken" binding:"required"`
}

// VerifyPasswordReq is the request payload for verifying the current
// user's login password (lock screen unlock flow).
type VerifyPasswordReq struct {
	Password string `json:"password" binding:"required"`
}
