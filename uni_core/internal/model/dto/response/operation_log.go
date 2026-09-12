package response

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// OperationLogResp is the response payload for a single operation log record.
type OperationLogResp struct {
	ID             uint64        `json:"id,string"`
	UserID         uint64        `json:"userId,string"`
	Username       string        `json:"username"`
	Module         string        `json:"module"`
	OperationType  string        `json:"operationType"`
	RequestMethod  string        `json:"requestMethod"`
	RequestURL     string        `json:"requestUrl"`
	RequestParams  string        `json:"requestParams"`
	ResponseResult string        `json:"responseResult"`
	CostTime       int           `json:"costTime"`
	IP             string        `json:"ip"`
	Code           int           `json:"code"`
	ErrorMsg       string        `json:"errorMsg"`
	OperTime       util.JSONTime `json:"operTime"`
}
