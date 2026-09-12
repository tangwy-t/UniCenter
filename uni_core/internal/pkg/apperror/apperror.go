// Package apperror provides structured application error types with HTTP status mapping.
package apperror

import (
	"fmt"
	"net/http"
)

// Error code constants for consistent error identification across the application.
const (
	CodeOK               = 0
	CodeUnauthorized     = 10001
	CodeForbidden        = 10002
	CodeTokenExpired     = 10003
	CodeCaptchaRequired  = 10004
	CodeCaptchaIncorrect = 10005
	CodeCaptchaExpired   = 10006
	CodeRateLimited      = 10007
	CodeAccountLocked    = 10008
	CodeBadRequest       = 40000
	CodeNotFound         = 40400
	CodeConflict         = 40900
	CodeInternal         = 50000
)

// AppError represents a structured application error with an error code,
// human-readable message, associated HTTP status, and optional cause.
type AppError struct {
	Code       int    `json:"code"`
	Message    string `json:"msg"`
	HTTPStatus int    `json:"-"`
	Data       any    `json:"data,omitempty"`
	cause      error
}

// Error returns a string representation including the error code and message.
func (e *AppError) Error() string {
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

// Unwrap returns the underlying cause of the error, supporting errors.Is/As chains.
func (e *AppError) Unwrap() error {
	return e.cause
}

// Unauthorized creates a 401 AppError with the given message.
func Unauthorized(msg string) *AppError {
	return &AppError{Code: CodeUnauthorized, Message: msg, HTTPStatus: http.StatusUnauthorized}
}

// Forbidden creates a 403 AppError with the given message.
func Forbidden(msg string) *AppError {
	return &AppError{Code: CodeForbidden, Message: msg, HTTPStatus: http.StatusForbidden}
}

// TokenExpired creates a 401 AppError indicating the token has expired.
func TokenExpired(msg string) *AppError {
	return &AppError{Code: CodeTokenExpired, Message: msg, HTTPStatus: http.StatusUnauthorized}
}

// CaptchaRequired creates a 401 AppError indicating captcha is required.
func CaptchaRequired(msg string) *AppError {
	return &AppError{Code: CodeCaptchaRequired, Message: msg, HTTPStatus: http.StatusUnauthorized}
}

// CaptchaIncorrect creates a 400 AppError indicating captcha answer is wrong.
func CaptchaIncorrect(msg string) *AppError {
	return &AppError{Code: CodeCaptchaIncorrect, Message: msg, HTTPStatus: http.StatusBadRequest}
}

// CaptchaExpired creates a 400 AppError indicating captcha has expired.
func CaptchaExpired(msg string) *AppError {
	return &AppError{Code: CodeCaptchaExpired, Message: msg, HTTPStatus: http.StatusBadRequest}
}

// AccountLocked creates a 401 AppError indicating the account is locked due to
// too many failed login attempts. remainingSeconds is the time until the lockout
// expires; the message displays the ceiling in minutes.
func AccountLocked(remainingSeconds int) *AppError {
	minutes := (remainingSeconds + 59) / 60
	return &AppError{
		Code:       CodeAccountLocked,
		Message:    fmt.Sprintf("账号已锁定，请 %d 分钟后重试", minutes),
		HTTPStatus: http.StatusUnauthorized,
		Data:       map[string]int{"lockRemaining": remainingSeconds},
	}
}

// RateLimited creates a 429 AppError indicating too many requests.
func RateLimited(msg string) *AppError {
	return &AppError{Code: CodeRateLimited, Message: msg, HTTPStatus: http.StatusTooManyRequests}
}

// BadRequest creates a 400 AppError with the given message.
func BadRequest(msg string) *AppError {
	return &AppError{Code: CodeBadRequest, Message: msg, HTTPStatus: http.StatusBadRequest}
}

// NotFound creates a 404 AppError with the given message.
func NotFound(msg string) *AppError {
	return &AppError{Code: CodeNotFound, Message: msg, HTTPStatus: http.StatusNotFound}
}

// Conflict creates a 409 AppError with the given message.
func Conflict(msg string) *AppError {
	return &AppError{Code: CodeConflict, Message: msg, HTTPStatus: http.StatusConflict}
}

// Internal creates a 500 AppError with the given message and optional underlying cause.
func Internal(msg string, cause ...error) *AppError {
	var c error
	if len(cause) > 0 {
		c = cause[0]
	}
	return &AppError{Code: CodeInternal, Message: msg, HTTPStatus: http.StatusInternalServerError, cause: c}
}
