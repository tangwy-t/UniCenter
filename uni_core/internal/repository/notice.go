package repository

import (
	"context"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/database"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type NoticeRepo struct {
	db *gorm.DB
}

func NewNoticeRepository(db *gorm.DB) *NoticeRepo {
	return &NoticeRepo{db: db}
}

func (r *NoticeRepo) applyFilters(db *gorm.DB, query *request.NoticeQuery) *gorm.DB {
	if query.Title != "" {
		db = db.Where("title LIKE ?", "%"+query.Title+"%")
	}
	if query.NoticeType != nil {
		db = db.Where("notice_type = ?", *query.NoticeType)
	}
	if query.Status != nil {
		db = db.Where("status = ?", *query.Status)
	}
	return db
}

func (r *NoticeRepo) FindPage(ctx context.Context, query *request.NoticeQuery) ([]entity.SysNotice, int64, error) {
	db := r.applyFilters(r.db.WithContext(ctx).Model(&entity.SysNotice{}), query)
	return paginate[entity.SysNotice](db, db.Order("id DESC"), query)
}

func (r *NoticeRepo) FindByID(ctx context.Context, id uint64) (*entity.SysNotice, error) {
	var notice entity.SysNotice
	if err := r.db.WithContext(ctx).First(&notice, id).Error; err != nil {
		return nil, err
	}
	return &notice, nil
}

func (r *NoticeRepo) Create(ctx context.Context, notice *entity.SysNotice) error {
	return r.db.WithContext(ctx).Create(notice).Error
}

func (r *NoticeRepo) Update(ctx context.Context, notice *entity.SysNotice) error {
	return r.db.WithContext(ctx).Model(&entity.SysNotice{}).Where("id = ?", notice.ID).Updates(notice).Error
}

func (r *NoticeRepo) Delete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).Delete(&entity.SysNotice{}, id).Error
}

func (r *NoticeRepo) FindMyNotices(ctx context.Context, userID uint64) ([]entity.SysNotice, error) {
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	var notices []entity.SysNotice
	err := r.db.WithContext(ctx).Table("sys_notice n").
		Joins("LEFT JOIN sys_notice_user nu ON n.id = nu.notice_id AND nu.user_id = ?", userID).
		Where("n.status = ?", entity.NoticeStatusPublished).
		Where("n.deleted_at IS NULL").
		Where("n.publish_time >= ?", monthStart).
		Where("(n.publish_type = ? OR (n.publish_type = ? AND nu.id IS NOT NULL))",
			entity.NoticePublishTypeAll, entity.NoticePublishTypeCustom).
		Where("nu.read_status IS NULL OR nu.read_status = ?", entity.NoticeReadStatusUnread).
		Order("n.publish_time DESC").
		Find(&notices).Error
	if err != nil {
		return nil, err
	}
	return notices, nil
}

// FindMyNoticesWithRead 查询用户公告收件箱(白名单接口 /notices/my 数据源):
// 已发布、当前用户可见(全员发布,或指定范围且存在 sys_notice_user 记录)的
// 公告,附带该用户的阅读状态。与 FindMyNotices 的区别是不限本月、且包含已读。
func (r *NoticeRepo) FindMyNoticesWithRead(ctx context.Context, userID uint64) ([]entity.NoticeWithRead, error) {
	// 显式 Select + 中间结构扫描:别名表(Postgres)下不能依赖 GORM 自动列限定,
	// 且目标字段含指针类型,统一走原始列扫描(与 FindReadUsers 同模式)。
	var rows []struct {
		ID          uint64
		Title       string
		Content     *string
		NoticeType  *int8
		Priority    *int8
		PublishTime *time.Time
		ReadStatus  *int8
		ReadTime    *time.Time
	}
	err := r.db.WithContext(ctx).Table("sys_notice n").
		Select("n.id, n.title, n.content, n.notice_type, n.priority, n.publish_time, "+
			"nu.read_status, nu.read_time").
		Joins("LEFT JOIN sys_notice_user nu ON n.id = nu.notice_id AND nu.user_id = ?", userID).
		Where("n.status = ?", entity.NoticeStatusPublished).
		Where("n.deleted_at IS NULL").
		Where("(n.publish_type = ? OR (n.publish_type = ? AND nu.id IS NOT NULL))",
			entity.NoticePublishTypeAll, entity.NoticePublishTypeCustom).
		Where("nu.deleted_at IS NULL OR nu.id IS NULL").
		Order("n.publish_time DESC, n.id DESC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	result := make([]entity.NoticeWithRead, 0, len(rows))
	for _, row := range rows {
		result = append(result, entity.NoticeWithRead{
			SysNotice: entity.SysNotice{
				BaseEntity:  entity.BaseEntity{ID: row.ID},
				Title:       row.Title,
				Content:     row.Content,
				NoticeType:  row.NoticeType,
				Priority:    row.Priority,
				PublishTime: row.PublishTime,
			},
			ReadStatus: row.ReadStatus,
			ReadTime:   row.ReadTime,
		})
	}
	return result, nil
}

// PublishTx 在单事务内完成"公告置为已发布"与"批量写入收件人关联":
// 两段写分离时,第二段失败会留下"已发布但无收件人"的中间态——
// 发布守卫(已发布不可重发)让重试永久 400,收件人永久丢失。
func (r *NoticeRepo) PublishTx(ctx context.Context, notice *entity.SysNotice, records []entity.SysNoticeUser) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&entity.SysNotice{}).Where("id = ?", notice.ID).Updates(notice).Error; err != nil {
			return err
		}
		if len(records) == 0 {
			return nil
		}
		return tx.Create(&records).Error
	})
}

func (r *NoticeRepo) UpsertNoticeUser(ctx context.Context, record *entity.SysNoticeUser) error {
	// 先把"未读"记录原子更新为已读:不依赖前置查询,消除先查后改窗口。
	res := r.db.WithContext(ctx).Model(&entity.SysNoticeUser{}).
		Where("notice_id = ? AND user_id = ? AND read_status = ?",
			record.NoticeID, record.UserID, entity.NoticeReadStatusUnread).
		Updates(map[string]interface{}{
			"read_status": entity.NoticeReadStatusRead,
			"read_time":   record.ReadTime,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}

	// 0 行:记录已存在且已读(幂等返回),或记录不存在(需创建)。
	var count int64
	if err := r.db.WithContext(ctx).Model(&entity.SysNoticeUser{}).
		Where("notice_id = ? AND user_id = ?", record.NoticeID, record.UserID).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil // already read
	}

	// 不存在则创建。并发下两个请求可能同时到达这里,后者撞
	// uk_notice_user 唯一键 —— 视为对方已完成,幂等成功。
	// (原实现先查后建,竞态时直接把 500 抛给用户。)
	if err := r.db.WithContext(ctx).Create(record).Error; err != nil {
		if database.IsDuplicateKey(err) {
			return nil
		}
		return err
	}
	return nil
}

// MarkAllRead 批量把用户可见公告全部标记已读,单事务内:
// 1) UPDATE 该用户现存未读行(指定范围发布的公告发布时已建行);
// 2) 对「已发布·全员·该用户尚无任意行」的公告补插已读行
//
//	(任意行即跳过:含历史软删行占用唯一键,避免撞 uk_notice_user)。
//
// 补插必须走 GORM Create:id 由全局 id:generate 回调填雪花 ID
// (sys_notice_user.id 无自增),再以 OnConflict 兜底并发幂等。
func (r *NoticeRepo) MarkAllRead(ctx context.Context, userID uint64) error {
	now := time.Now()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&entity.SysNoticeUser{}).
			Where("user_id = ? AND read_status = ?", userID, entity.NoticeReadStatusUnread).
			Updates(map[string]interface{}{
				"read_status": entity.NoticeReadStatusRead,
				"read_time":   now,
			}).Error; err != nil {
			return err
		}

		var ids []uint64
		selectSQL := `
			SELECT n.id FROM sys_notice n
			WHERE n.status = ? AND n.deleted_at IS NULL AND n.publish_type = ?
			  AND NOT EXISTS (
			    SELECT 1 FROM sys_notice_user nu
			    WHERE nu.notice_id = n.id AND nu.user_id = ?
			  )`
		if err := tx.Raw(selectSQL,
			entity.NoticeStatusPublished, entity.NoticePublishTypeAll, userID,
		).Scan(&ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}

		rows := make([]entity.SysNoticeUser, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, entity.SysNoticeUser{
				NoticeID:   id,
				UserID:     userID,
				ReadStatus: entity.NoticeReadStatusRead,
				ReadTime:   &now,
			})
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "notice_id"}, {Name: "user_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"read_status", "read_time"}),
		}).Create(&rows).Error
	})
}

// FindReadStatusMap 批量取当前用户在一组公告上的阅读状态:
// 只统计未软删的行,无行→map 缺键(调用方按"未读"消费)。
func (r *NoticeRepo) FindReadStatusMap(ctx context.Context, userID uint64, noticeIDs []uint64) (map[uint64]int8, error) {
	result := make(map[uint64]int8, len(noticeIDs))
	if userID == 0 || len(noticeIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		NoticeID   uint64
		ReadStatus int8
	}
	if err := r.db.WithContext(ctx).
		Table("sys_notice_user").
		Select("notice_id, read_status").
		Where("user_id = ? AND notice_id IN ? AND deleted_at IS NULL", userID, noticeIDs).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		result[rows[i].NoticeID] = rows[i].ReadStatus
	}
	return result, nil
}

func (r *NoticeRepo) FindUserIDsByRoleIDs(ctx context.Context, roleIDs []uint64) ([]uint64, error) {
	if len(roleIDs) == 0 {
		return nil, nil
	}
	var userIDs []uint64
	if err := r.db.WithContext(ctx).Model(&entity.SysUserRole{}).
		Where("role_id IN ?", roleIDs).Distinct().Pluck("user_id", &userIDs).Error; err != nil {
		return nil, err
	}
	return userIDs, nil
}

func (r *NoticeRepo) FindUserIDsByDeptIDs(ctx context.Context, deptIDs []uint64) ([]uint64, error) {
	if len(deptIDs) == 0 {
		return nil, nil
	}
	var userIDs []uint64
	if err := r.db.WithContext(ctx).Model(&entity.SysUser{}).
		Where("dept_id IN ?", deptIDs).Pluck("id", &userIDs).Error; err != nil {
		return nil, err
	}
	return userIDs, nil
}

// FindUserNamesByIDs 批量取用户登录名(列表"创建者"列渲染)。
func (r *NoticeRepo) FindUserNamesByIDs(ctx context.Context, ids []uint64) (map[uint64]string, error) {
	result := make(map[uint64]string, len(ids))
	if len(ids) == 0 {
		return result, nil
	}
	var rows []struct {
		ID       uint64
		Username string
	}
	if err := r.db.WithContext(ctx).Model(&entity.SysUser{}).
		Select("id", "username").
		Where("id IN ?", ids).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ID] = row.Username
	}
	return result, nil
}

// FindUsersByIDs 按 ID 反查用户(指定个人接收范围回显标签)。
func (r *NoticeRepo) FindUsersByIDs(ctx context.Context, ids []uint64) ([]entity.SysUser, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var users []entity.SysUser
	if err := r.db.WithContext(ctx).Model(&entity.SysUser{}).
		Where("id IN ?", ids).
		Order("id ASC").
		Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

// FindReadUsers 分页查询已读用户(阅读用户弹窗)。
func (r *NoticeRepo) FindReadUsers(ctx context.Context, noticeID uint64, query *request.NoticeReadUsersQuery) ([]response.NoticeReadUserResp, int64, error) {
	base := r.db.WithContext(ctx).
		Table("sys_notice_user nu").
		Joins("LEFT JOIN sys_user u ON u.id = nu.user_id").
		Joins("LEFT JOIN sys_dept d ON d.id = u.dept_id").
		Where("nu.notice_id = ?", noticeID).
		Where("nu.read_status = ?", entity.NoticeReadStatusRead).
		Where("nu.deleted_at IS NULL").
		Where("u.deleted_at IS NULL")

	if query.SearchValue != "" {
		kw := "%" + query.SearchValue + "%"
		base = base.Where("(u.username LIKE ? OR u.real_name LIKE ?)", kw, kw)
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// 扫描到中间结构:JSONTime 未实现 sql.Scanner,不能直接作为扫描目标。
	var rows []struct {
		UserID   uint64
		Username string
		RealName string
		DeptName string
		Phone    string
		ReadTime *time.Time
	}
	if err := base.
		Select("nu.user_id AS user_id",
			"u.username AS username",
			"COALESCE(u.real_name, '') AS real_name",
			"COALESCE(d.name, '') AS dept_name",
			"COALESCE(u.phone, '') AS phone",
			"nu.read_time AS read_time").
		Order("(nu.read_time IS NULL), nu.read_time DESC").
		Limit(query.GetPageSize()).
		Offset(query.Offset()).
		Scan(&rows).Error; err != nil {
		return nil, 0, err
	}

	resp := make([]response.NoticeReadUserResp, 0, len(rows))
	for _, row := range rows {
		item := response.NoticeReadUserResp{
			UserID:   row.UserID,
			Username: row.Username,
			RealName: row.RealName,
			DeptName: row.DeptName,
			Phone:    row.Phone,
		}
		if row.ReadTime != nil {
			rt := util.JSONTime(*row.ReadTime)
			item.ReadTime = &rt
		}
		resp = append(resp, item)
	}
	return resp, total, nil
}
