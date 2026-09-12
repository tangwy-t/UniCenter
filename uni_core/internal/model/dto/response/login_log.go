package response

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// LoginLogResp is the response payload for a single login log record.
type LoginLogResp struct {
	ID        uint64        `json:"id,string"`
	UserID    uint64        `json:"userId,string"`
	Username  string        `json:"username"`
	IP        string        `json:"ip"`
	Location  string        `json:"location"`
	Browser   string        `json:"browser"`
	OS        string        `json:"os"`
	Code      int           `json:"code"`
	Msg       string        `json:"msg"`
	LoginTime util.JSONTime `json:"loginTime"`
}
