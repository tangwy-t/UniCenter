package request

import (
	"time"

	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
)

// OperationLogQuery holds the query parameters for paginated operation log listing.
type OperationLogQuery struct {
	app.PageRequest
	Username      string `form:"username"`
	Module        string `form:"module"`
	OperationType string `form:"operationType"`
	Code          *int   `form:"code"`
	StartTime     string `form:"startTime"`
	EndTime       string `form:"endTime"`
}

// DeleteOperationLogReq is the request payload for cleaning operation logs before a given date.
type DeleteOperationLogReq struct {
	Before time.Time `form:"before" time_format:"2006-01-02" binding:"required"`
}
