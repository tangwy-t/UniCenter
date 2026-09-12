package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
	"github.com/tangwy-t/webmanager-server/internal/pkg/ws"
	wsPkg "github.com/tangwy-t/webmanager-server/internal/pkg/ws"

	"go.uber.org/zap"
)

// EventBusInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
type EventBusInterface interface {
	PublishNotice(ctx context.Context, evt *ws.PushEvent) error
}

// NoticeRepositoryInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
type NoticeRepositoryInterface interface {
	FindPage(ctx context.Context, query *request.NoticeQuery) ([]entity.SysNotice, int64, error)
	FindByID(ctx context.Context, id uint64) (*entity.SysNotice, error)
	Create(ctx context.Context, notice *entity.SysNotice) error
	Update(ctx context.Context, notice *entity.SysNotice) error
	Delete(ctx context.Context, id uint64) error
	FindMyNotices(ctx context.Context, userID uint64) ([]entity.SysNotice, error)
	FindMyNoticesWithRead(ctx context.Context, userID uint64) ([]entity.NoticeWithRead, error)
	// PublishTx 单事务完成"置为已发布"+"写入收件人关联"。
	PublishTx(ctx context.Context, notice *entity.SysNotice, records []entity.SysNoticeUser) error
	UpsertNoticeUser(ctx context.Context, record *entity.SysNoticeUser) error
	// MarkAllRead 批量把当前用户可见公告全部标记已读(仓库层单事务,幂等)。
	MarkAllRead(ctx context.Context, userID uint64) error
	FindReadStatusMap(ctx context.Context, userID uint64, noticeIDs []uint64) (map[uint64]int8, error)
	FindUserIDsByRoleIDs(ctx context.Context, roleIDs []uint64) ([]uint64, error)
	FindUserIDsByDeptIDs(ctx context.Context, deptIDs []uint64) ([]uint64, error)
	FindUserNamesByIDs(ctx context.Context, ids []uint64) (map[uint64]string, error)
	FindUsersByIDs(ctx context.Context, ids []uint64) ([]entity.SysUser, error)
	FindReadUsers(ctx context.Context, noticeID uint64, query *request.NoticeReadUsersQuery) ([]response.NoticeReadUserResp, int64, error)
}

type NoticeService struct {
	repo     NoticeRepositoryInterface
	eventBus EventBusInterface
	logger   logger.LoggerInterface
}

func NewNoticeService(repo NoticeRepositoryInterface, eventBus EventBusInterface, logger logger.LoggerInterface) *NoticeService {
	return &NoticeService{repo: repo, eventBus: eventBus, logger: logger}
}

func (s *NoticeService) FindPage(ctx context.Context, query *request.NoticeQuery) (*app.PageResponse, error) {
	list, total, err := s.repo.FindPage(ctx, query)
	if err != nil {
		s.logger.Error("NoticeService.FindPage failed", zap.Error(err))
		return nil, apperror.Internal("查询通知公告失败")
	}
	resp := make([]response.NoticeResp, 0, len(list))
	for i := range list {
		resp = append(resp, s.buildResp(&list[i]))
	}
	s.attachCreators(ctx, list, resp)
	s.attachReadStatus(ctx, list, resp)
	return app.NewPageResponse(resp, total, query.GetPage(), query.GetPageSize()), nil
}

func (s *NoticeService) FindByID(ctx context.Context, id uint64) (*response.NoticeResp, error) {
	notice, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, translateNotFound(err, "通知公告不存在")
	}
	resp := s.buildResp(notice)
	s.attachCreators(ctx, []entity.SysNotice{*notice}, []response.NoticeResp{resp})
	return &resp, nil
}

// buildResp 组装响应:TargetDesc 由 TargetType/TargetIDs 计算,遗留数据按发布方式兜底。
func (s *NoticeService) buildResp(notice *entity.SysNotice) response.NoticeResp {
	resp := util.MapEntity[response.NoticeResp](notice, s.logger)
	resp.TargetDesc = s.targetDesc(notice)
	return resp
}

// attachReadStatus 为"已发布"行补当前用户阅读状态:有行→行值(0/1),无行→0 未读;
// 草稿/已撤回保持 nil。ctx 无登录用户时跳过(全 nil)。
func (s *NoticeService) attachReadStatus(ctx context.Context, list []entity.SysNotice, resp []response.NoticeResp) {
	userID, ok := contextkeys.UserIDFromCtx(ctx)
	if !ok || userID == 0 {
		return
	}
	ids := make([]uint64, 0, len(list))
	for i := range list {
		if list[i].Status != nil && *list[i].Status == entity.NoticeStatusPublished {
			ids = append(ids, list[i].ID)
		}
	}
	readMap := map[uint64]int8{}
	if len(ids) > 0 {
		var err error
		readMap, err = s.repo.FindReadStatusMap(ctx, userID, ids)
		if err != nil {
			s.logger.Error("NoticeService.FindPage read map failed", zap.Error(err))
			return
		}
	}
	for i := range list {
		if list[i].Status != nil && *list[i].Status == entity.NoticeStatusPublished {
			v := readMap[list[i].ID] // 无行=0 未读
			resp[i].ReadStatus = &v
		}
	}
}

// attachCreators 批量把 CreatedBy(用户 ID)替换为登录名,供"创建者"列渲染。
func (s *NoticeService) attachCreators(ctx context.Context, entities []entity.SysNotice, resp []response.NoticeResp) {
	need := make(map[uint64]struct{})
	for i := range entities {
		if entities[i].CreatedBy != nil && *entities[i].CreatedBy != 0 {
			need[*entities[i].CreatedBy] = struct{}{}
		}
	}
	if len(need) == 0 {
		return
	}
	ids := make([]uint64, 0, len(need))
	for id := range need {
		ids = append(ids, id)
	}
	names, err := s.repo.FindUserNamesByIDs(ctx, ids)
	if err != nil {
		s.logger.Warn("attachCreators: find user names failed", zap.Error(err))
		return
	}
	for i := range entities {
		if entities[i].CreatedBy != nil {
			resp[i].CreateBy = names[*entities[i].CreatedBy]
		}
	}
}

// targetTypeNames 接收范围类型 → 中文前缀。
var targetTypeNames = map[int8]string{
	entity.NoticeTargetTypeAll:  "全体成员",
	entity.NoticeTargetTypeRole: "指定角色",
	entity.NoticeTargetTypeDept: "指定部门",
	entity.NoticeTargetTypeUser: "指定个人",
}

// targetDesc 计算接收范围展示文案(如 指定角色（2个）)。
// 遗留数据(未存 TargetType)按发布方式兜底。
func (s *NoticeService) targetDesc(notice *entity.SysNotice) string {
	if notice.TargetType == nil {
		if notice.PublishType != nil && *notice.PublishType == entity.NoticePublishTypeCustom {
			return "指定成员"
		}
		return targetTypeNames[entity.NoticeTargetTypeAll]
	}
	name, ok := targetTypeNames[*notice.TargetType]
	if !ok {
		return targetTypeNames[entity.NoticeTargetTypeAll]
	}
	if *notice.TargetType == entity.NoticeTargetTypeAll {
		return name
	}
	if n := util.CSVUint64Count(notice.TargetIDs); n > 0 {
		return fmt.Sprintf("%s（%d个）", name, n)
	}
	return name
}

// applyTargetScope 校验接收范围并据其派生发布方式:
// 全体成员 → 全员发布,其余 → 自定义范围发布。
func (s *NoticeService) applyTargetScope(notice *entity.SysNotice, targetType *int8, targetIDs string) error {
	if targetType == nil {
		return nil // 旧客户端未传:维持原样
	}
	if _, ok := targetTypeNames[*targetType]; !ok {
		return apperror.BadRequest("接收范围参数错误")
	}
	trimmed := strings.TrimSpace(targetIDs)
	if *targetType != entity.NoticeTargetTypeAll {
		ids, ok := util.ParseCSVUint64s(trimmed)
		if !ok {
			return apperror.BadRequest("接收对象 ID 参数错误")
		}
		if len(ids) == 0 {
			return apperror.BadRequest("请选择接收对象")
		}
		// 规范化:去掉空白,统一逗号分隔
		parts := make([]string, 0, len(ids))
		for _, id := range ids {
			parts = append(parts, strconv.FormatUint(id, 10))
		}
		trimmed = strings.Join(parts, ",")
	}
	notice.TargetType = util.Ptr[int8](*targetType)
	notice.TargetIDs = trimmed
	publishType := entity.NoticePublishTypeAll
	if *targetType != entity.NoticeTargetTypeAll {
		publishType = entity.NoticePublishTypeCustom
	}
	notice.PublishType = util.Ptr[int8](publishType)
	return nil
}

func (s *NoticeService) Create(ctx context.Context, req *request.CreateNoticeReq) (uint64, error) {
	notice := &entity.SysNotice{}
	util.CopyEntity(notice, req, s.logger)
	notice.Status = util.Ptr[int8](entity.NoticeStatusDraft)

	if err := s.applyTargetScope(notice, req.TargetType, req.TargetIDs); err != nil {
		return 0, err
	}

	if err := s.repo.Create(ctx, notice); err != nil {
		s.logger.Error("NoticeService.Create failed", zap.Error(err))
		return 0, apperror.Internal("创建通知公告失败")
	}
	s.logger.Info("notice created", zap.Uint64("noticeId", notice.ID), zap.String("title", notice.Title))
	return notice.ID, nil
}

func (s *NoticeService) Update(ctx context.Context, id uint64, req *request.UpdateNoticeReq) error {
	existing, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "通知公告不存在")
	}
	if existing.Status != nil && *existing.Status == entity.NoticeStatusPublished {
		return apperror.BadRequest("已发布的通知不能修改")
	}

	util.CopyEntity(existing, req, s.logger)

	if err := s.applyTargetScope(existing, req.TargetType, req.TargetIDs); err != nil {
		return err
	}

	if err := s.repo.Update(ctx, existing); err != nil {
		s.logger.Error("NoticeService.Update failed", zap.Error(err))
		return apperror.Internal("更新通知公告失败")
	}
	s.logger.Info("notice updated", zap.Uint64("noticeId", id))
	return nil
}

func (s *NoticeService) Delete(ctx context.Context, id uint64) error {
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return translateNotFound(err, "通知公告不存在")
	}

	if err := s.repo.Delete(ctx, id); err != nil {
		s.logger.Error("NoticeService.Delete failed", zap.Error(err))
		return apperror.Internal("删除通知公告失败")
	}
	s.logger.Info("notice deleted", zap.Uint64("noticeId", id))
	return nil
}

func (s *NoticeService) Publish(ctx context.Context, id uint64, req *request.PublishNoticeReq) error {
	notice, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "通知公告不存在")
	}
	if notice.Status != nil && *notice.Status == entity.NoticeStatusPublished {
		return apperror.BadRequest("通知公告已发布")
	}

	now := time.Now()

	publishType := entity.NoticePublishTypeAll
	if notice.PublishType != nil {
		publishType = *notice.PublishType
	}
	notice.Status = util.Ptr[int8](entity.NoticeStatusPublished)
	notice.PublishTime = &now

	// For custom publish, resolve target users (reads only, before the tx)
	var records []entity.SysNoticeUser
	userIDSet := make(map[uint64]struct{})
	if publishType == entity.NoticePublishTypeCustom {
		// 请求未显式指定收件人时,回退到创建/编辑时保存的接收范围(TargetType/TargetIDs)。
		if len(req.UserIDs) == 0 && len(req.RoleIDs) == 0 && len(req.DeptIDs) == 0 {
			if notice.TargetType != nil {
				ids, ok := util.ParseCSVUint64s(notice.TargetIDs)
				if !ok {
					return apperror.BadRequest("接收对象 ID 参数错误")
				}
				switch *notice.TargetType {
				case entity.NoticeTargetTypeRole:
					req.RoleIDs = ids
				case entity.NoticeTargetTypeDept:
					req.DeptIDs = ids
				case entity.NoticeTargetTypeUser:
					req.UserIDs = ids
				}
			}
		}

		// Add explicit user IDs
		for _, uid := range []uint64(req.UserIDs) {
			userIDSet[uid] = struct{}{}
		}

		// Resolve users by role IDs
		if len(req.RoleIDs) > 0 {
			roleUserIDs, err := s.repo.FindUserIDsByRoleIDs(ctx, []uint64(req.RoleIDs))
			if err != nil {
				s.logger.Error("NoticeService.Publish FindUserIDsByRoleIDs failed", zap.Error(err))
				return apperror.Internal("解析角色关联用户失败")
			}
			for _, uid := range roleUserIDs {
				userIDSet[uid] = struct{}{}
			}
		}

		// Resolve users by dept IDs
		if len(req.DeptIDs) > 0 {
			deptUserIDs, err := s.repo.FindUserIDsByDeptIDs(ctx, []uint64(req.DeptIDs))
			if err != nil {
				s.logger.Error("NoticeService.Publish FindUserIDsByDeptIDs failed", zap.Error(err))
				return apperror.Internal("解析部门关联用户失败")
			}
			for _, uid := range deptUserIDs {
				userIDSet[uid] = struct{}{}
			}
		}

		// Build NoticeUser records (write happens in the tx below)
		if len(userIDSet) > 0 {
			records = make([]entity.SysNoticeUser, 0, len(userIDSet))
			for uid := range userIDSet {
				records = append(records, entity.SysNoticeUser{
					NoticeID:   id,
					UserID:     uid,
					ReadStatus: entity.NoticeReadStatusUnread,
				})
			}
		} else {
			return apperror.BadRequest("自定义范围未配置接收人，请先编辑接收范围")
		}
	}

	// 单事务完成两段写:此前"置为已发布"与"写入收件人"分离,第二段
	// 失败留下已发布但无收件人的中间态——已发布守卫使重试永久 400,
	// 收件人永久丢失。
	if err := s.repo.PublishTx(ctx, notice, records); err != nil {
		s.logger.Error("NoticeService.Publish failed", zap.Error(err))
		return apperror.Internal("发布通知公告失败")
	}

	// WebSocket real-time push via Redis event bus
	noticeResp := s.buildResp(notice)

	noticeData, err := json.Marshal(noticeResp)
	if err != nil {
		s.logger.Warn("NoticeService.Publish marshal notice for push failed", zap.Error(err))
	} else {
		evt := &wsPkg.PushEvent{
			Type:       wsPkg.MsgTypeNewNotice,
			NoticeData: noticeData,
		}
		if publishType == entity.NoticePublishTypeAll {
			evt.IsAll = true
		} else {
			evt.IsAll = false
			evt.UserIDs = make([]uint64, 0, len(userIDSet))
			for uid := range userIDSet {
				evt.UserIDs = append(evt.UserIDs, uid)
			}
		}
		// Publish to Redis; all instances (including this one) receive and push to local clients
		if err := s.eventBus.PublishNotice(ctx, evt); err != nil {
			s.logger.Warn("NoticeService.Publish publish to event bus failed", zap.Error(err))
		}
	}

	return nil
}

func (s *NoticeService) Revoke(ctx context.Context, id uint64) error {
	notice, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return translateNotFound(err, "通知公告不存在")
	}
	if notice.Status == nil || *notice.Status != entity.NoticeStatusPublished {
		return apperror.BadRequest("只有已发布的通知才能撤销")
	}

	notice.Status = util.Ptr[int8](entity.NoticeStatusRevoked)
	if err := s.repo.Update(ctx, notice); err != nil {
		s.logger.Error("NoticeService.Revoke failed", zap.Error(err))
		return apperror.Internal("撤销通知公告失败")
	}
	return nil
}

func (s *NoticeService) MarkRead(ctx context.Context, noticeID, userID uint64) error {
	notice, err := s.repo.FindByID(ctx, noticeID)
	if err != nil {
		return translateNotFound(err, "通知公告不存在")
	}
	if notice.Status == nil || *notice.Status != entity.NoticeStatusPublished {
		return apperror.BadRequest("只能阅读已发布的通知")
	}

	now := time.Now()
	record := &entity.SysNoticeUser{
		NoticeID:   noticeID,
		UserID:     userID,
		ReadStatus: entity.NoticeReadStatusRead,
		ReadTime:   &now,
	}

	if err := s.repo.UpsertNoticeUser(ctx, record); err != nil {
		s.logger.Error("NoticeService.MarkRead failed", zap.Error(err))
		return apperror.Internal("标记已读失败")
	}
	return nil
}

// MarkAllRead 批量已读(白名单接口 /notices/read-all):纯仓库透传,
// 错误统一映射 500("标记全部已读失败"),会话用户由调用方校验并传入。
func (s *NoticeService) MarkAllRead(ctx context.Context, userID uint64) error {
	if err := s.repo.MarkAllRead(ctx, userID); err != nil {
		s.logger.Error("NoticeService.MarkAllRead failed", zap.Error(err))
		return apperror.Internal("标记全部已读失败")
	}
	return nil
}

// FindReadUsers 分页查询已读用户(阅读用户弹窗)。
func (s *NoticeService) FindReadUsers(ctx context.Context, noticeID uint64, query *request.NoticeReadUsersQuery) (*app.PageResponse, error) {
	if _, err := s.repo.FindByID(ctx, noticeID); err != nil {
		return nil, translateNotFound(err, "通知公告不存在")
	}
	rows, total, err := s.repo.FindReadUsers(ctx, noticeID, query)
	if err != nil {
		s.logger.Error("NoticeService.FindReadUsers failed", zap.Error(err))
		return nil, apperror.Internal("查询已读用户失败")
	}
	return app.NewPageResponse(rows, total, query.GetPage(), query.GetPageSize()), nil
}

// FindTargetUsers 按 ID 反查指定个人(接收范围回显)。
func (s *NoticeService) FindTargetUsers(ctx context.Context, idsCSV string) ([]response.NoticeTargetUserResp, error) {
	ids, ok := util.ParseCSVUint64s(idsCSV)
	if !ok {
		return nil, apperror.BadRequest("参数错误")
	}
	if len(ids) == 0 {
		return []response.NoticeTargetUserResp{}, nil
	}
	users, err := s.repo.FindUsersByIDs(ctx, ids)
	if err != nil {
		s.logger.Error("NoticeService.FindTargetUsers failed", zap.Error(err))
		return nil, apperror.Internal("查询接收人失败")
	}
	// 保序回显
	order := make(map[uint64]int, len(ids))
	for i, id := range ids {
		order[id] = i
	}
	rows := make([]response.NoticeTargetUserResp, 0, len(users))
	for _, u := range users {
		realName := ""
		if u.RealName != nil {
			realName = *u.RealName
		}
		rows = append(rows, response.NoticeTargetUserResp{
			ID:       u.ID,
			Username: u.Username,
			RealName: realName,
		})
	}
	sort.SliceStable(rows, func(a, b int) bool {
		return order[rows[a].ID] < order[rows[b].ID]
	})
	return rows, nil
}

// GetUnreadNotices returns unread notices for a user as JSON RawMessage slices.
// Used by the WebSocket Hub for catch-up push when a user comes online.
func (s *NoticeService) GetUnreadNotices(ctx context.Context) ([]json.RawMessage, error) {
	userID, ok := contextkeys.UserIDFromCtx(ctx)
	if !ok {
		return nil, nil
	}
	notices, err := s.repo.FindMyNotices(ctx, userID)
	if err != nil {
		return nil, err
	}

	var result []json.RawMessage
	for i := range notices {
		resp := s.buildResp(&notices[i])
		data, err := json.Marshal(resp)
		if err != nil {
			s.logger.Warn("NoticeService.GetUnreadNotices marshal failed", zap.Error(err))
			continue
		}
		result = append(result, data)
	}
	return result, nil
}

// MyNotices 用户公告收件箱(白名单接口 /notices/my,对齐若依 listTop):
// 返回当前用户可见的已发布公告(含已读标记)与未读数,不做分页
// —— 收件箱数据量小,全量下发由前端渲染。
func (s *NoticeService) MyNotices(ctx context.Context) (*response.NoticeMyListResp, error) {
	userID, ok := contextkeys.UserIDFromCtx(ctx)
	if !ok {
		return nil, apperror.Unauthorized("未登录或 token 已过期")
	}
	rows, err := s.repo.FindMyNoticesWithRead(ctx, userID)
	if err != nil {
		s.logger.Error("NoticeService.MyNotices failed", zap.Error(err))
		return nil, apperror.Internal("查询我的公告失败")
	}

	resp := &response.NoticeMyListResp{List: make([]response.NoticeMyItemResp, 0, len(rows))}
	for i := range rows {
		row := &rows[i]
		noticeType := entity.NoticeTypeNotice
		if row.NoticeType != nil {
			noticeType = *row.NoticeType
		}
		priority := entity.NoticePriorityNormal
		if row.Priority != nil {
			priority = *row.Priority
		}
		item := response.NoticeMyItemResp{
			ID:         row.ID,
			Title:      row.Title,
			NoticeType: noticeType,
			Priority:   priority,
			IsRead:     row.ReadStatus != nil && *row.ReadStatus == entity.NoticeReadStatusRead,
		}
		if row.Content != nil {
			item.Content = *row.Content
		}
		if row.PublishTime != nil {
			pt := util.JSONTime(*row.PublishTime)
			item.PublishTime = &pt
		}
		if !item.IsRead {
			resp.UnreadCount++
		}
		resp.List = append(resp.List, item)
	}
	return resp, nil
}
