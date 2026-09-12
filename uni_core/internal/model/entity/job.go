package entity

type SysJob struct {
	BaseEntity
	Name           string  `gorm:"column:name;size:64;not null"              json:"name"`
	JobGroup       string  `gorm:"column:job_group;size:64;not null"         json:"jobGroup"`
	CronExpression string  `gorm:"column:cron_expression;size:128;not null"  json:"cronExpression"`
	InvokeTarget   string  `gorm:"column:invoke_target;size:256;not null"    json:"invokeTarget"`
	InvokeParams   *string `gorm:"column:invoke_params;size:512"             json:"invokeParams"`
	Concurrent     *int8   `gorm:"column:concurrent;default:1"               json:"concurrent"`
	RetryCount     *int    `gorm:"column:retry_count;default:0"              json:"retryCount"`
	RetryInterval  *int    `gorm:"column:retry_interval;default:0"           json:"retryInterval"`
	Status         *int8   `gorm:"column:status;default:1"                   json:"status"`
	RunAtStartup   *int8   `gorm:"column:run_at_startup;default:0"           json:"runAtStartup"`
	Remark         *string `gorm:"column:remark;size:512"                    json:"remark"`
}

// Kept for backward compatibility.
const (
	JobStatusEnabled        int8 = 1
	JobStatusPaused         int8 = 0
	JobConcurrentAllowed    int8 = 1
	JobConcurrentDisallowed int8 = 0
	JobRunAtStartupYes      int8 = 1
	JobRunAtStartupNo       int8 = 0
)

func (SysJob) TableName() string { return "sys_job" }
