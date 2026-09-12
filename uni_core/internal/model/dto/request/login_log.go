package request

import "github.com/tangwy-t/webmanager-server/internal/pkg/app"

// LoginLogQuery holds the query parameters for paginated login log listing.
type LoginLogQuery struct {
	app.PageRequest
	Username  string `form:"username"`
	IP        string `form:"ip"`
	Code      *int   `form:"code"`
	StartTime string `form:"startTime"`
	EndTime   string `form:"endTime"`
}
