// Package snowflake provides a distributed unique ID generator based on Twitter's Snowflake algorithm.
package snowflake

import (
	"fmt"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"

	"go.uber.org/zap"

	sf "github.com/bwmarrin/snowflake"
)

// New creates a snowflake Node with the given workerID (valid range: 0-1023).
func New(workerID int64, logger logger.LoggerInterface) (*sf.Node, error) {
	node, err := sf.NewNode(workerID)
	if err != nil {
		logger.Error("failed to init snowflake", zap.Error(err), zap.Int64("workerId", workerID))
		return nil, fmt.Errorf("failed to init snowflake: %w", err)
	}
	logger.Info("Snowflake initialized", zap.Int64("workerId", workerID))
	return node, nil
}
