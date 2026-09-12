package response

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// JobResp 定时任务响应。
type JobResp struct {
	ID             uint64         `json:"id,string"`
	Name           string         `json:"name"`
	JobGroup       string         `json:"jobGroup"`
	CronExpression string         `json:"cronExpression"`
	InvokeTarget   string         `json:"invokeTarget"`
	InvokeParams   string         `json:"invokeParams"`
	Concurrent     int8           `json:"concurrent"`
	RetryCount     int            `json:"retryCount"`
	RetryInterval  int            `json:"retryInterval"`
	Status         int8           `json:"status"`
	RunAtStartup   int8           `json:"runAtStartup"`
	Remark         string         `json:"remark"`
	NextRunTime    *util.JSONTime `json:"nextRunTime"`
	CreatedAt      util.JSONTime  `json:"createdAt"`
	UpdatedAt      util.JSONTime  `json:"updatedAt"`
}

// JobLogResp 任务执行日志响应。
type JobLogResp struct {
	ID           uint64         `json:"id,string"`
	JobID        uint64         `json:"jobId,string"`
	JobName      string         `json:"jobName"`
	JobGroup     string         `json:"jobGroup"`
	InvokeTarget string         `json:"invokeTarget"`
	TriggerType  int8           `json:"triggerType"`
	StartTime    util.JSONTime  `json:"startTime"`
	EndTime      *util.JSONTime `json:"endTime"`
	CostTime     int            `json:"costTime"`
	Status       int8           `json:"status"`
	ErrorMsg     string         `json:"errorMsg"`
}

// JobTargetResp 可用任务目标响应。
type JobTargetResp struct {
	Target      string         `json:"target"`
	DisplayName string         `json:"displayName"`
	HasParams   bool           `json:"hasParams"`
	ParamSchema map[string]any `json:"paramSchema,omitempty"`
}

// JobHealthResp 调度器健康状态响应。
type JobHealthResp struct {
	SchedulerRunning bool   `json:"schedulerRunning"`
	InstanceID       string `json:"instanceId"`
	TotalJobs        int    `json:"totalJobs"`
	RunningJobs      int    `json:"runningJobs"`
	Uptime           string `json:"uptime"`
}
