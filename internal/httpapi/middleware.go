package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

type ctxKey int

const metaKey ctxKey = 1

// reqMeta 通过指针挂在 context 上，使外层 accessLog 与内层 ServeMux
// 在多次 WithContext 浅拷贝后仍能共享 route 模板与 request_id。
type reqMeta struct {
	RequestID string
	Route     string
}

func metaFrom(ctx context.Context) *reqMeta {
	m, _ := ctx.Value(metaKey).(*reqMeta)
	return m
}

// RequestIDFromContext 返回当前请求的关联 ID；缺失时为空串。
func RequestIDFromContext(ctx context.Context) string {
	if m := metaFrom(ctx); m != nil {
		return m.RequestID
	}
	return ""
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 极端情况下退化为时间派生，仍保持 32 位小写十六进制形态。
		sum := sha256.Sum256([]byte(time.Now().String()))
		return hex.EncodeToString(sum[:16])
	}
	return hex.EncodeToString(b[:])
}

func hashAPIKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

func isPublicPath(path string) bool {
	switch path {
	case "/health/live", "/health/ready", "/metrics":
		return true
	default:
		return false
	}
}

func isBusinessPath(path string) bool {
	switch path {
	case "/api/v1/retail/summary", "/api/v1/renovation/funnel":
		return true
	default:
		return false
	}
}

// withRequestID 为每个请求分配 16 字节随机 request_id（小写 hex），写入响应头，
// 并放入共享 meta，供错误信封与访问日志关联，而不依赖客户端传入值。
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		meta := &reqMeta{RequestID: id}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), metaKey, meta)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withRecovery 捕获业务与中间件 panic：完整堆栈只进服务端日志，对外仅 INTERNAL_ERROR，
// 避免把内部实现细节或敏感路径泄漏给调用方。
func withRecovery(logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger.Error("panic recovered",
					"request_id", requestIDFor(w, r),
					"panic", fmt.Sprint(rec),
					"stack", string(debug.Stack()),
				)
				writeError(w, r, logger, http.StatusInternalServerError, codeInternalError, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withAccessLog 记录方法、路由模板、状态与耗时；故意不记录原始 URL 与任何请求头，
// 防止 query 中的 org_id 高基数标签化，以及 X-API-Key 明文进入集中日志。
func withAccessLog(logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		route := "unknown"
		if m := metaFrom(r.Context()); m != nil && m.Route != "" {
			route = m.Route
		}
		logger.Info("http_request",
			"request_id", RequestIDFromContext(r.Context()),
			"method", r.Method,
			"route", route,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

// withTimeout 仅为业务查询派生子 context；健康检查与指标端点不受 QUERY_TIMEOUT 约束，
// 以免探活被慢查询超时误杀。
func withTimeout(d time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d <= 0 || !isBusinessPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// withAPIKey 将配置密钥与请求头都先做 SHA-256 再 subtle.ConstantTimeCompare，
// 使比较时间与密钥长度差无关，降低侧信道猜测风险；失败时不返回 WWW-Authenticate，
// 避免暗示认证方案细节。健康与指标路径跳过鉴权，便于探针与 Prometheus 抓取。
func withAPIKey(expectedDigest []byte, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		presented := hashAPIKey(r.Header.Get("X-API-Key"))
		if subtle.ConstantTimeCompare(expectedDigest, presented) != 1 {
			writeError(w, r, logger, http.StatusUnauthorized, codeUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// captureRoute 在 ServeMux 匹配后把 Pattern 写回共享 meta，供外层 accessLog 使用路由模板。
func captureRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if m := metaFrom(r.Context()); m != nil && r.Pattern != "" {
			m.Route = r.Pattern
		}
	})
}
