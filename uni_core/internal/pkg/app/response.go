// Package app provides HTTP response helpers for Gin-based handlers.
package app

import (
	"errors"
	"log"
	"net/http"
	"sync"

	"github.com/tangwy-t/webmanager-server/internal/pkg/apperror"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// Response is the standard JSON envelope for all API responses.
type Response struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
	Data    any    `json:"data"`
}

// errorLogger is installed once at startup via SetLogger (from router.Setup).
// Unknown (non-AppError) errors reaching a client are logged with request
// context so production 500s can be attributed after the fact.
var (
	errorLoggerMu sync.RWMutex
	errorLogger   logger.LoggerInterface
)

// SetLogger installs the logger used by Error for unhandled errors.
func SetLogger(l logger.LoggerInterface) {
	errorLoggerMu.Lock()
	errorLogger = l
	errorLoggerMu.Unlock()
}

// currentErrorLogger snapshots the installed logger under RLock. Before
// router.Setup runs it is nil and Error falls back to the standard library
// logger, so early-startup 500s are not silently swallowed.
func currentErrorLogger() logger.LoggerInterface {
	errorLoggerMu.RLock()
	defer errorLoggerMu.RUnlock()
	return errorLogger
}

// Success writes a 200 OK response with the given data payload.
func Success(c *gin.Context, data any) {
	c.JSON(http.StatusOK, Response{Code: apperror.CodeOK, Message: "success", Data: data})
}

// Error writes an error response derived from the error type.
// If err is an *apperror.AppError, its HTTP status and code are used;
// otherwise the error is logged (via the logger installed by SetLogger) and a
// generic 500 response is returned that leaks no internal detail.
// AppError with 5xx status is also logged (with its cause chain): business
// 4xx (not found / bad request) is noise, but a silent 500 is unattributable.
func Error(c *gin.Context, err error) {
	var appErr *apperror.AppError
	if errors.As(err, &appErr) {
		if appErr.HTTPStatus >= http.StatusInternalServerError {
			if l := currentErrorLogger(); l != nil {
				l.Error("internal error returned to client",
					zap.Int("code", appErr.Code),
					zap.String("msg", appErr.Message),
					zap.Error(err),
					zap.String("method", c.Request.Method),
					zap.String("path", c.Request.URL.Path))
			} else {
				log.Printf("[app] internal error returned to client (logger not installed): %v %s %s",
					err, c.Request.Method, c.Request.URL.Path)
			}
		}
		c.JSON(appErr.HTTPStatus, Response{Code: appErr.Code, Message: appErr.Message, Data: appErr.Data})
		return
	}
	if l := currentErrorLogger(); l != nil {
		l.Error("unhandled error returned to client",
			zap.Error(err),
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path))
	} else {
		log.Printf("[app] unhandled error returned to client (logger not installed): %v %s %s",
			err, c.Request.Method, c.Request.URL.Path)
	}
	c.JSON(http.StatusInternalServerError, Response{Code: apperror.CodeInternal, Message: "internal server error", Data: nil})
}
