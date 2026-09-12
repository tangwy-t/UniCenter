package entity

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/datascope/rule"
	"time"
)

type SysOperationLog struct {
	ID             uint64    `gorm:"column:id;primaryKey;autoIncrement:false" json:"id,string"`
	UserID         uint64    `gorm:"column:user_id;not null"                   json:"userId,string"`
	Username       string    `gorm:"column:username;size:64;not null"          json:"username"`
	Module         string    `gorm:"column:module;size:64;not null"            json:"module"`
	OperationType  string    `gorm:"column:operation_type;size:64;not null"    json:"operationType"`
	RequestMethod  *string   `gorm:"column:request_method;size:10"             json:"requestMethod"`
	RequestURL     *string   `gorm:"column:request_url;size:256"               json:"requestUrl"`
	RequestParams  *string   `gorm:"column:request_params;type:text"           json:"requestParams"`
	ResponseResult *string   `gorm:"column:response_result;type:text"          json:"responseResult"`
	CostTime       *int      `gorm:"column:cost_time;default:0"                json:"costTime"`
	IP             *string   `gorm:"column:ip;size:64"                         json:"ip"`
	Code           int       `gorm:"column:code;not null;default:0"            json:"code"`
	ErrorMsg       *string   `gorm:"column:error_msg;size:1024"                json:"errorMsg"`
	OperTime       time.Time `gorm:"column:oper_time;not null;index:idx_op_log_oper_time" json:"operTime"`
}

func (SysOperationLog) TableName() string { return "sys_operation_log" }

// DataScopeRules implements rule.DataScopeable.
func (SysOperationLog) DataScopeRules() []rule.ScopeRule {
	return []rule.ScopeRule{
		{Column: "user_id", DimensionType: "dept", Via: &rule.ScopeVia{Table: "sys_user", Select: "id", Where: "dept_id"}},
		{Column: "user_id", DimensionType: "self"},
	}
}
