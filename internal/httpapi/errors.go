package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/woo721/cursor_test/internal/domain"
)

const (
	codeInvalidArgument       = "INVALID_ARGUMENT"
	codeUnauthorized          = "UNAUTHORIZED"
	codeDependencyUnavailable = "DEPENDENCY_UNAVAILABLE"
	codeQueryTimeout          = "QUERY_TIMEOUT"
	codeInternalError         = "INTERNAL_ERROR"
)

type errorEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func requestIDFor(w http.ResponseWriter, r *http.Request) string {
	if id := RequestIDFromContext(r.Context()); id != "" {
		return id
	}
	// recovery 位于 requestID 之外，panic 路径上的 Request 可能尚未带上 meta；
	// 回退读取已写入的响应头，保证错误信封与 X-Request-ID 一致。
	return w.Header().Get("X-Request-ID")
}

// writeError 写出稳定的 JSON 错误信封：固定用户可读文案、单次 WriteHeader，
// 绝不回传 SQL/DSN/驱动细节；编码失败只记日志，避免二次写响应造成协议错乱。
func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, code, message string) {
	if logger == nil {
		logger = slog.Default()
	}
	reqID := requestIDFor(w, r)
	var env errorEnvelope
	env.Error.Code = code
	env.Error.Message = message
	env.Error.RequestID = reqID

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(env); err != nil {
		logger.Error("encode error response failed",
			"request_id", reqID,
			"err", err.Error(),
		)
	}
}

func mapDomainError(err error) (status int, code, message string) {
	switch {
	case errors.Is(err, domain.ErrInvalidArgument):
		return http.StatusBadRequest, codeInvalidArgument, "invalid argument"
	case errors.Is(err, domain.ErrDependencyUnavailable):
		return http.StatusServiceUnavailable, codeDependencyUnavailable, "dependency unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, codeQueryTimeout, "query timed out"
	default:
		return http.StatusInternalServerError, codeInternalError, "internal error"
	}
}

func writeJSON(w http.ResponseWriter, r *http.Request, logger *slog.Logger, status int, v any) {
	if logger == nil {
		logger = slog.Default()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Error("encode json response failed",
			"request_id", RequestIDFromContext(r.Context()),
			"err", err.Error(),
		)
	}
}
