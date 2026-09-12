package request

import "github.com/tangwy-t/webmanager-server/internal/pkg/app"

// JobQuery 定时任务列表查询参数。
type JobQuery struct {
	app.PageRequest
	Name     string `form:"name"`
	JobGroup string `form:"jobGroup"`
	Status   *int8  `form:"status"`
}

// CreateJobReq 创建定时任务请求。
type CreateJobReq struct {
	Name           string  `json:"name" binding:"required,max=64"`
	JobGroup       string  `json:"jobGroup" binding:"required,max=64"`
	CronExpression string  `json:"cronExpression" binding:"required,max=128"`
	InvokeTarget   string  `json:"invokeTarget" binding:"required,max=256"`
	InvokeParams   *string `json:"invokeParams"`
	Concurrent     *int8   `json:"concurrent"`
	RetryCount     *int    `json:"retryCount"`
	RetryInterval  *int    `json:"retryInterval"`
	Status         *int8   `json:"status"`
	RunAtStartup   *int8   `json:"runAtStartup"`
	Remark         *string `json:"remark"`
}

// UpdateJobReq 更新定时任务请求。
type UpdateJobReq struct {
	CronExpression string  `json:"cronExpression" binding:"required,max=128"`
	InvokeTarget   string  `json:"invokeTarget" binding:"required,max=256"`
	InvokeParams   *string `json:"invokeParams"`
	Concurrent     *int8   `json:"concurrent"`
	RetryCount     *int    `json:"retryCount"`
	RetryInterval  *int    `json:"retryInterval"`
	Status         *int8   `json:"status"`
	RunAtStartup   *int8   `json:"runAtStartup"`
	Remark         *string `json:"remark"`
}

// JobStatusReq 更新任务状态请求（Pause/Resume 共用）。
type JobStatusReq struct {
	Status *int8 `json:"status" binding:"required"`
}

// JobLogQuery 任务执行日志查询参数。
type JobLogQuery struct {
	app.PageRequest
	JobID     uint64 `form:"jobId"`
	Status    *int8  `form:"status"`
	StartTime string `form:"startTime"`
	EndTime   string `form:"endTime"`
}
