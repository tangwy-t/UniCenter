// Package loggertest provides a shared test logger factory for unit tests
// across the internal/pkg packages, eliminating duplicate newTestLogger helpers.
package loggertest

import (
	"github.com/tangwy-t/webmanager-server/internal/pkg/config"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
)

// New returns a debug-level Logger suitable for use in unit tests.
// The returned logger satisfies logger.LoggerInterface.
func New() *logger.Logger {
	l, _ := logger.New(&config.LogConfig{
		Level:       "debug",
		Format:      "json",
		Output:      "stdout",
		Development: true,
		Caller:      false,
	})
	return l
}
