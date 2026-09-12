package service

import (
	"context"
	"errors"
	"time"

	"github.com/tangwy-t/webmanager-server/internal/model/dto/request"
	"github.com/tangwy-t/webmanager-server/internal/model/dto/response"
	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/app"
	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// OperationLogRepositoryInterface 由 service/interface.go 迁移至此:接口定义在消费方,
// 不再维护包级中央接口文件。
// OperationLogRepositoryInterface defines the data-access contract for operation log operations.
type OperationLogRepositoryInterface interface {
	// FindPage returns a paginated list of operation logs matching the given query, along with the total count.
	FindPage(ctx context.Context, query *request.OperationLogQuery) ([]entity.SysOperationLog, int64, error)
	// Create inserts a new operation log record.
	Create(ctx context.Context, log *entity.SysOperationLog) error
	// DeleteBefore removes operation log records whose oper_time is before the given time.
	DeleteBefore(ctx context.Context, before time.Time) (int64, error)
}

type OperationLogService struct {
	repo     OperationLogRepositoryInterface
	userRepo UserRepositoryInterface
	logger   logger.LoggerInterface
}

// NewOperationLogService constructs an OperationLogService with the given dependencies.
func NewOperationLogService(
	repo OperationLogRepositoryInterface,
	userRepo UserRepositoryInterface,
	logger logger.LoggerInterface,
) *OperationLogService {
	return &OperationLogService{
		repo:     repo,
		userRepo: userRepo,
		logger:   logger,
	}
}

func (s *OperationLogService) FindPage(ctx context.Context, query *request.OperationLogQuery) (*app.PageResponse, error) {
	logs, total, err := s.repo.FindPage(ctx, query)
	if err != nil {
		return nil, err
	}
	list := make([]response.OperationLogResp, 0, len(logs))
	for i := range logs {
		list = append(list, util.MapEntity[response.OperationLogResp](&logs[i], s.logger))
	}
	return app.NewPageResponse(list, total, query.GetPage(), query.GetPageSize()), nil
}

func (s *OperationLogService) Create(ctx context.Context, log *entity.SysOperationLog) error {
	// Resolve username from userID if not already set.
	if log.Username == "" && log.UserID != 0 {
		user, err := s.userRepo.FindByID(ctx, log.UserID)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			s.logger.Warn("failed to resolve username for operation log",
				zap.Uint64("userId", log.UserID), zap.Error(err))
		}
		if user != nil {
			log.Username = user.Username
		}
	}
	return s.repo.Create(ctx, log)
}

func (s *OperationLogService) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	count, err := s.repo.DeleteBefore(ctx, before)
	if err != nil {
		return 0, apperror.Internal("删除操作日志失败")
	}
	return count, nil
}
