package repository

import (
	"context"

	"gorm.io/gorm"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
)

// defaultPartitionLogLimit 是 ListByTable 在 limit<=0 时用的上限。
//
// 为什么给默认值而不是「0 = 不限」：审计表**不清理**，行数只增不减；
// 「把这张表的流水全列出来」在跑了三年之后就是一次无界读。而 limit=0 最可能的
// 意思是「忘了填」，不是「我要全部」——静默变成全表扫描正是最难在测试里发现的形态。
const defaultPartitionLogLimit = 200

// AgentPartitionLogRepo 是分区维护审计表（spec §7.3）的数据访问层。
type AgentPartitionLogRepo struct {
	db *gorm.DB
}

func NewAgentPartitionLogRepository(db *gorm.DB) *AgentPartitionLogRepo {
	return &AgentPartitionLogRepo{db: db}
}

// Insert 写入一条审计流水。
//
// **单条、独立提交**：本方法既不开事务、签名里也**没有**任何事务句柄
// （不接受 `*gorm.DB`/`tx` 参数）—— 这就是 spec §7.3「ADD 与 DROP 分两次提交，
// 永不合并」在类型层面的落点：DDL 是隐式提交语句，把审计写入塞进 DDL 的事务
// 本就不可能（DDL 早已提交），唯一的效果是让审计行连同它的失败一起消失。
// 因此「审计与 DDL 共事务」这条路在这里**不存在**，而不是靠注释提醒。
//
// 返回的 error 原样上抛（不吞、不转 sentinel）：调用方（分区协调器）要能把它写进
// 日志与 ErrPartitionPartial；审计失败**不得**触发任何对 DDL 的撤销动作。
func (r *AgentPartitionLogRepo) Insert(ctx context.Context, row *entity.AgentPartitionLog) error {
	return r.db.WithContext(ctx).Create(row).Error
}

// ListByTable 按表读回审计流水（缺口归因用：「这张图的这一段为什么是空的」）。
//
// 排序是**写入顺序**（created_at 升序，同刻按 id 升序）：审计是流水，
// 读回的顺序即发生顺序；倒序（最近优先）是展示层的选择，不该固化在仓储里
// （id 是雪花号，本身就单调递增，故同刻并列时按 id 仍是确定的）。
//
// table 为空 = 不过滤（运维排障常常要「整个库最近发生了什么」）；
// limit<=0 用 defaultPartitionLogLimit —— 见该常量的说明。
func (r *AgentPartitionLogRepo) ListByTable(ctx context.Context, table string, limit int) ([]entity.AgentPartitionLog, error) {
	if limit <= 0 {
		limit = defaultPartitionLogLimit
	}
	q := r.db.WithContext(ctx).Order("created_at ASC, id ASC").Limit(limit)
	if table != "" {
		q = q.Where("table_name = ?", table)
	}
	var out []entity.AgentPartitionLog
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}
