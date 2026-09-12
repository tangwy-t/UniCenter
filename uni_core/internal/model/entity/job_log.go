package entity

import "time"

type SysJobLog struct {
	ID           uint64     `gorm:"column:id;primaryKey;autoIncrement:false" json:"id,string"`
	JobID        uint64     `gorm:"column:job_id;not null;index:idx_job_log_job_id" json:"jobId,string"`
	JobName      string     `gorm:"column:job_name;size:64;not null"         json:"jobName"`
	JobGroup     string     `gorm:"column:job_group;size:64;not null"        json:"jobGroup"`
	InvokeTarget string     `gorm:"column:invoke_target;size:256;not null"   json:"invokeTarget"`
	TriggerType  int8       `gorm:"column:trigger_type;default:1"            json:"triggerType"`
	StartTime    time.Time  `gorm:"column:start_time;not null;index:idx_job_log_start_time" json:"startTime"`
	EndTime      *time.Time `gorm:"column:end_time"                          json:"endTime"`
	CostTime     *int       `gorm:"column:cost_time;default:0"               json:"costTime"`
	Status       int8       `gorm:"column:status;not null;default:1"         json:"status"`
	ErrorMsg     *string    `gorm:"column:error_msg;size:1024"               json:"errorMsg"`
}

// Kept for backward compatibility.
const (
	JobLogTriggerCron   int8 = 1 // 定时触发
	JobLogTriggerManual int8 = 2 // 手动执行一次
	JobLogStatusRunning int8 = 0 // 执行中
	JobLogStatusSuccess int8 = 1 // 成功
	JobLogStatusFailed  int8 = 2 // 失败
)

func (SysJobLog) TableName() string { return "sys_job_log" }
