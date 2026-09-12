package response

import "github.com/tangwy-t/webmanager-server/internal/pkg/util"

// LoginResp is the response payload returned after a successful login.
type LoginResp struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
}

// RefreshTokenResp is the response payload returned after a successful token refresh.
type RefreshTokenResp struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
}

// AvatarUploadResp is the response payload after a successful avatar upload.
// Avatar is a server-root path (including the API prefix, e.g.
// "/api/v1/user/avatar/8?v=1757040000") usable by <img> tags on deployments
// where the web origin fronts the API (same-origin or /api reverse proxy).
type AvatarUploadResp struct {
	Avatar string `json:"avatar"`
}

// UserInfoResp contains the current user's profile, roles, and permissions.
type UserInfoResp struct {
	ID                 uint64   `json:"id,string"`
	Username           string   `json:"username"`
	RealName           string   `json:"realName"`
	Nickname           string   `json:"nickname"`
	Gender             string   `json:"gender"`
	Avatar             string   `json:"avatar"`
	Email              string   `json:"email"`
	Phone              string   `json:"phone"`
	DataScope          int8     `json:"dataScope"`     // 新增
	DeptID             uint64   `json:"deptId,string"` // 新增
	Roles              []string `json:"roles"`
	Permissions        []string `json:"permissions"`
	MustChangePassword bool     `json:"mustChangePassword"`
}

// RoleBriefResp is a role's code + display name pair (个人中心用名称展示,code 供前端权限判断)。
type RoleBriefResp struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// LoginStatsResp aggregates a user's login counters for the personal-center
// vitals readout.
type LoginStatsResp struct {
	TotalLogins int64 `json:"totalLogins"` // 历史成功登录总次数
	Logins30d   int64 `json:"logins30d"`   // 近 30 天成功登录次数
	Failed30d   int64 `json:"failed30d"`   // 近 30 天失败尝试次数
}

// DailyActivityResp is one day's login tally in the 30-day pulse strip.
// Date is "2006-01-02" in server-local time; the series is always
// zero-filled to 30 consecutive days ending today.
type DailyActivityResp struct {
	Date    string `json:"date"`
	Success int64  `json:"success"`
	Failed  int64  `json:"failed"`
}

// RecentLoginResp is one recent login attempt (logout records excluded).
// Code 0 = success; non-zero = apperror business code, Msg carries the reason.
type RecentLoginResp struct {
	Time    util.JSONTime `json:"time"`
	IP      string        `json:"ip"`
	Browser string        `json:"browser"`
	OS      string        `json:"os"`
	Code    int           `json:"code"`
	Msg     string        `json:"msg"`
}

// UserOverviewResp is the personal-center aggregate payload: identity with
// display names, login vitals, the 30-day activity series and recent login
// attempts. Served by GET /user/overview; strictly self-scoped.
type UserOverviewResp struct {
	ID            uint64              `json:"id,string"`
	Username      string              `json:"username"`
	RealName      string              `json:"realName"`
	Avatar        string              `json:"avatar"`
	Email         string              `json:"email"`
	Phone         string              `json:"phone"`
	DeptName      string              `json:"deptName"`
	Roles         []RoleBriefResp     `json:"roles"`
	CreatedAt     util.JSONTime       `json:"createdAt"`
	LastLoginTime *util.JSONTime      `json:"lastLoginTime"`
	LastLoginIP   string              `json:"lastLoginIp"`
	Stats         LoginStatsResp      `json:"stats"`
	DailyActivity []DailyActivityResp `json:"dailyActivity"`
	RecentLogins  []RecentLoginResp   `json:"recentLogins"`
}

// VerifyPasswordResp tells whether the provided password matches the
// current user's login password.
type VerifyPasswordResp struct {
	Valid bool `json:"valid"`
}
