package service

import (
	"context"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

// LoginLogRepositoryInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
// LoginLogRepositoryInterface defines the data-access contract for login log operations.
type LoginLogRepositoryInterface interface {
	// FindPage returns a paginated list of login logs matching the given query, along with the total count.
	FindPage(ctx context.Context, query *request.LoginLogQuery) ([]entity.SysLoginLog, int64, error)
	// Create inserts a new login log record.
	Create(ctx context.Context, log *entity.SysLoginLog) error
	// DeleteBefore removes login log records whose login_time is before the given time.
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}

type LoginLogService struct {
	repo   LoginLogRepositoryInterface
	logger logger.LoggerInterface
}

// NewLoginLogService constructs a LoginLogService with the given dependencies.
func NewLoginLogService(
	repo LoginLogRepositoryInterface,
	logger logger.LoggerInterface,
) *LoginLogService {
	return &LoginLogService{
		repo:   repo,
		logger: logger,
	}
}

func (s *LoginLogService) FindPage(ctx context.Context, query *request.LoginLogQuery) (*app.PageResponse, error) {
	logs, total, err := s.repo.FindPage(ctx, query)
	if err != nil {
		return nil, err
	}
	list := make([]response.LoginLogResp, 0, len(logs))
	for i := range logs {
		list = append(list, util.MapEntity[response.LoginLogResp](&logs[i], s.logger))
	}
	return app.NewPageResponse(list, total, query.GetPage(), query.GetPageSize()), nil
}

// RecordLogin 写入一条登录日志。code 为业务结果码:0=登录成功(CodeOK),
// 失败时为 apperror 包的具体业务码(10001 用户名或密码错误、10005 验证码错误、
// 10008 账号已锁定等,完整取值见字典 sys_opt_result_code),msg 为补充文案。
func (s *LoginLogService) RecordLogin(ctx context.Context, userID uint64, username, ip, userAgent string, code int, msg string) error {
	browser, os := util.ParseUserAgent(userAgent)

	var uid *uint64
	if userID > 0 {
		uid = &userID
	}

	var ipPtr *string
	if ip != "" {
		ipPtr = &ip
	}

	var msgPtr *string
	if msg != "" {
		msgPtr = &msg
	}

	entry := &entity.SysLoginLog{
		UserID:    uid,
		Username:  username,
		IP:        ipPtr,
		Browser:   browser,
		OS:        os,
		Code:      code,
		Msg:       msgPtr,
		LoginTime: time.Now(),
	}

	return s.repo.Create(ctx, entry)
}

func (s *LoginLogService) RecordLogout(ctx context.Context, userID uint64, username, ip string) error {
	msg := "登出成功"
	var uid *uint64
	if userID > 0 {
		uid = &userID
	}

	var ipPtr *string
	if ip != "" {
		ipPtr = &ip
	}

	entry := &entity.SysLoginLog{
		UserID:    uid,
		Username:  username,
		IP:        ipPtr,
		Code:      apperror.CodeOK,
		Msg:       &msg,
		LoginTime: time.Now(),
	}

	return s.repo.Create(ctx, entry)
}
