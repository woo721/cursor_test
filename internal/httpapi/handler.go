package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/woo721/cursor_test/internal/app"
	"github.com/woo721/cursor_test/internal/domain"
)

// QueryService 是 httpapi 对应用层查询能力的窄接口，便于用假实现做契约测试。
type QueryService interface {
	RetailSummary(ctx context.Context, source domain.Source, query domain.Query) (app.RetailResult, error)
	RenovationFunnel(ctx context.Context, source domain.Source, query domain.Query) (app.RenovationResult, error)
}

// Readiness 描述就绪探针依赖；真实模式由 bootstrap 组合多源 Ping。
type Readiness interface {
	Ping(context.Context) error
}

// Dependencies 装配 HTTP 层所需协作对象；配置项由调用方注入，避免本包再读环境变量。
type Dependencies struct {
	Queries        QueryService
	Ready          Readiness
	APIKey         string
	QueryTimeout   time.Duration
	ReadyTimeout   time.Duration
	MaxQueryDays   int
	Timezone       *time.Location
	Logger         *slog.Logger
	MetricsHandler http.Handler
}

type api struct {
	queries      QueryService
	ready        Readiness
	maxDays      int
	tz           *time.Location
	logger       *slog.Logger
	readyTimeout time.Duration
}

type parsedQuery struct {
	Source domain.Source
	Query  domain.Query
}

type segmentDTO struct {
	Source    string `json:"source"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
}

type metaDTO struct {
	RequestID       string       `json:"request_id"`
	RequestedSource string       `json:"requested_source"`
	Segments        []segmentDTO `json:"segments"`
}

type retailDataDTO struct {
	OrderCount  int64  `json:"order_count"`
	SalesAmount string `json:"sales_amount"`
}

type retailResponse struct {
	Data retailDataDTO `json:"data"`
	Meta metaDTO       `json:"meta"`
}

type renovationDataDTO struct {
	LeadCount      int64 `json:"lead_count"`
	InvitedCount   int64 `json:"invited_count"`
	MeasuredCount  int64 `json:"measured_count"`
	SignedCount    int64 `json:"signed_count"`
	StartedCount   int64 `json:"started_count"`
	CompletedCount int64 `json:"completed_count"`
}

type renovationResponse struct {
	Data renovationDataDTO `json:"data"`
	Meta metaDTO           `json:"meta"`
}

// New 组装路由与中间件：recovery(requestID(accessLog(timeout(apiKey(routes)))))。
func New(deps Dependencies) (http.Handler, error) {
	if deps.APIKey == "" {
		return nil, errors.New("APIKey: required")
	}

	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	readyTimeout := deps.ReadyTimeout
	if readyTimeout <= 0 {
		readyTimeout = 2 * time.Second
	}
	tz := deps.Timezone
	if tz == nil {
		tz = time.UTC
	}
	a := &api{
		queries:      deps.Queries,
		ready:        deps.Ready,
		maxDays:      deps.MaxQueryDays,
		tz:           tz,
		logger:       logger,
		readyTimeout: readyTimeout,
	}
	if a.maxDays < 1 {
		a.maxDays = 366
	}

	metrics := deps.MetricsHandler
	if metrics == nil {
		// Task 7 会注入 Prometheus handler；此处保留无鉴权占位，保证探针契约先稳定。
		metrics = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/retail/summary", a.retailSummary)
	mux.HandleFunc("GET /api/v1/renovation/funnel", a.renovationFunnel)
	mux.HandleFunc("GET /health/live", a.live)
	mux.HandleFunc("GET /health/ready", a.readyCheck)
	mux.Handle("GET /metrics", metrics)

	routes := captureRoute(mux)
	keyDigest := hashAPIKey(deps.APIKey)

	return withRecovery(logger,
		withRequestID(
			withAccessLog(logger,
				withTimeout(deps.QueryTimeout,
					withAPIKey(keyDigest, logger, routes),
				),
			),
		),
	), nil
}

func (a *api) parseQuery(r *http.Request) (parsedQuery, error) {
	q := r.URL.Query()
	source, err := domain.ParseSource(q.Get("source"))
	if err != nil {
		return parsedQuery{}, err
	}
	if err := domain.ValidateOrgID(q.Get("org_id")); err != nil {
		return parsedQuery{}, err
	}
	dr, err := domain.NewDateRange(q.Get("start_date"), q.Get("end_date"), a.maxDays, a.tz)
	if err != nil {
		return parsedQuery{}, err
	}
	return parsedQuery{
		Source: source,
		Query: domain.Query{
			Range: dr,
			OrgID: q.Get("org_id"),
		},
	}, nil
}

func segmentsDTO(segments []domain.Segment) []segmentDTO {
	out := make([]segmentDTO, 0, len(segments))
	for _, s := range segments {
		out = append(out, segmentDTO{
			Source:    string(s.Source),
			StartDate: s.Range.StartString(),
			EndDate:   s.Range.EndString(),
		})
	}
	return out
}

func (a *api) writeMappedError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message := mapDomainError(err)
	a.logger.Error("request failed",
		"request_id", RequestIDFromContext(r.Context()),
		"code", code,
		"err", err.Error(),
	)
	writeError(w, r, a.logger, status, code, message)
}

func (a *api) retailSummary(w http.ResponseWriter, r *http.Request) {
	pq, err := a.parseQuery(r)
	if err != nil {
		writeError(w, r, a.logger, http.StatusBadRequest, codeInvalidArgument, "invalid argument")
		return
	}
	if a.queries == nil {
		writeError(w, r, a.logger, http.StatusInternalServerError, codeInternalError, "internal error")
		return
	}
	result, err := a.queries.RetailSummary(r.Context(), pq.Source, pq.Query)
	if err != nil {
		a.writeMappedError(w, r, err)
		return
	}
	writeJSON(w, r, a.logger, http.StatusOK, retailResponse{
		Data: retailDataDTO{
			OrderCount:  result.Data.OrderCount,
			SalesAmount: result.Data.SalesAmount.StringFixed(2),
		},
		Meta: metaDTO{
			RequestID:       RequestIDFromContext(r.Context()),
			RequestedSource: string(pq.Source),
			Segments:        segmentsDTO(result.Segments),
		},
	})
}

func (a *api) renovationFunnel(w http.ResponseWriter, r *http.Request) {
	pq, err := a.parseQuery(r)
	if err != nil {
		writeError(w, r, a.logger, http.StatusBadRequest, codeInvalidArgument, "invalid argument")
		return
	}
	if a.queries == nil {
		writeError(w, r, a.logger, http.StatusInternalServerError, codeInternalError, "internal error")
		return
	}
	result, err := a.queries.RenovationFunnel(r.Context(), pq.Source, pq.Query)
	if err != nil {
		a.writeMappedError(w, r, err)
		return
	}
	writeJSON(w, r, a.logger, http.StatusOK, renovationResponse{
		Data: renovationDataDTO{
			LeadCount:      result.Data.LeadCount,
			InvitedCount:   result.Data.InvitedCount,
			MeasuredCount:  result.Data.MeasuredCount,
			SignedCount:    result.Data.SignedCount,
			StartedCount:   result.Data.StartedCount,
			CompletedCount: result.Data.CompletedCount,
		},
		Meta: metaDTO{
			RequestID:       RequestIDFromContext(r.Context()),
			RequestedSource: string(pq.Source),
			Segments:        segmentsDTO(result.Segments),
		},
	})
}

func (a *api) live(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (a *api) readyCheck(w http.ResponseWriter, r *http.Request) {
	if a.ready == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.readyTimeout)
	defer cancel()
	if err := a.ready.Ping(ctx); err != nil {
		writeError(w, r, a.logger, http.StatusServiceUnavailable, codeDependencyUnavailable, "dependency unavailable")
		return
	}
	w.WriteHeader(http.StatusOK)
}
