# 家装行业 Go 数据查询 API 设计

## 1. 目标

建设一个可直接运行、可连接真实数据源、适合有其他后端经验的开发者学习 Go 完整开发流程的生产级示例项目。

项目面向家装企业经营分析，提供两类只读业务 API：

- 零售经营指标：有效订单数、含税销售额。
- 家装阶段发生量：留资、邀约、量房、签约、开工、竣工。

查询支持四种来源：

- `memory`：默认本地演示数据，不依赖外部集群。
- `hive`：Go 通过 HiveServer2 直接查询 Hive。
- `trino`：Go 通过 Trino 查询 Hive Catalog。
- `starrocks`：Go 通过 MySQL 协议查询 StarRocks。

默认 `source=auto`。历史数据查询 HiveServer2 或 Trino，最近 3 个自然日查询 StarRocks；跨边界请求切成两个分段并行查询，最后合并。

## 2. 设计原则

1. **业务 API 不暴露任意 SQL。** 调用方只能查询定义好的指标，避免 SQL 注入、越权访问和失控的大查询。
2. **业务规则不依赖数据库驱动。** 路由和合并逻辑只依赖 `QueryRepository` 接口。
3. **生产能力保持可理解。** 使用单进程模块化单体，不为示例提前拆分微服务。
4. **失败必须显式。** 跨引擎查询缺少任一分段时整体失败，不返回未标记的部分数据。
5. **业务口径优先于技术炫技。** 金额使用定点十进制，日期口径明确，阶段发生量不冒充 Cohort 转化率。
6. **注释解释“为什么”。** 对 Go 生命周期、接口边界、并发取消、连接池和 SQL 安全提供详细中文注释；不为显而易见的语句逐行复述。

## 3. 技术约束

- Go 版本：`1.26.5`。
- HTTP：Go 标准库 `net/http`，避免为两个查询端点引入大型 Web 框架。
- HiveServer2：`github.com/beltran/gohive/v2`，使用 `database/sql` 接口。
- Trino：官方 `github.com/trinodb/trino-go-client/trino` 驱动。
- StarRocks：`github.com/go-sql-driver/mysql`，使用 StarRocks 兼容的 MySQL 协议。
- 金额：`github.com/shopspring/decimal`，JSON 返回字符串以保持精度。
- 并发：`golang.org/x/sync/errgroup`。
- 配置：环境变量解析为强类型配置；启动时一次性校验。
- 日志：Go 标准库 `log/slog` 输出 JSON。
- 指标：Prometheus Go client。
- 测试：标准库 `testing`、`httptest` 与 `github.com/DATA-DOG/go-sqlmock`。

依赖在实施时通过 Go 包管理器选择当时最新稳定版本，不手写过期版本号。

## 4. 总体架构

项目采用 Ports and Adapters 风格的模块化单体：

```text
调用方
  |
  | HTTP + X-API-Key
  v
HTTP Transport
  |- panic recovery
  |- request ID
  |- JSON access log
  |- API key authentication
  |- request timeout
  v
Application Query Service
  |- 参数和业务范围校验
  |- 最近 3 日边界计算
  |- 分段与并行查询
  |- 指标合并
  v
QueryRepository interface
  |- Memory adapter
  |- HiveServer2 adapter
  |- Trino adapter
  `- StarRocks adapter
```

### 4.1 模块职责

- `domain`：日期范围、查询来源、零售指标、家装阶段指标和领域错误。不得导入数据库或 HTTP 包。
- `service`：自动路由、固定时钟、并发执行、取消和结果合并。
- `adapter`：连接配置、SQL 生成、执行、行扫描和驱动错误包装。
- `transport/http`：路由、请求 DTO、响应 DTO、中间件和错误映射。
- `config`：读取和校验环境变量，不包含业务逻辑。
- `cmd/server`：装配依赖、启动 HTTP 服务、处理信号和优雅停机。

## 5. 领域模型

### 5.1 日期范围

API 接收 `YYYY-MM-DD` 格式的 `start_date` 与 `end_date`，两端均包含。所有日期按 `APP_TIMEZONE` 解释，默认 `Asia/Shanghai`。

约束：

- `start_date` 不得晚于 `end_date`。
- 查询跨度不得超过 `MAX_QUERY_DAYS`，默认 366 天。
- 日期仅表示业务自然日，不使用调用方本地时区。

### 5.2 零售指标

```text
RetailSummary
  order_count   int64
  sales_amount  decimal.Decimal
```

- `order_count`：查询期内满足企业有效订单规则的订单数。
- `sales_amount`：这些订单的含税成交金额。

### 5.3 家装阶段指标

```text
RenovationFunnel
  lead_count       int64  留资
  invited_count    int64  邀约
  measured_count   int64  量房
  signed_count     int64  签约
  started_count    int64  开工
  completed_count  int64  竣工
```

这些值表示查询期内各阶段的发生量。不同阶段可能来自不同批次客户，因此本版不计算相邻阶段转化率。真正的转化率需要以同一批留资客户为 Cohort，属于后续独立能力。

### 5.4 查询来源

外部参数支持：

- `auto`：按数据新鲜度自动路由。
- `hive`：整个时间段直接查询 HiveServer2。
- `trino`：整个时间段通过 Trino 查询 Hive。
- `starrocks`：整个时间段查询 StarRocks。

`memory` 是应用运行模式，不作为生产查询参数暴露。在内存模式下，以上来源名称仍执行相同路由，但底层均由可标记来源的内存适配器响应，以便完整演示 API 行为。

## 6. 自动路由与合并

配置：

- `REALTIME_WINDOW_DAYS=3`：今天和前两个自然日属于实时区。
- `HISTORICAL_SOURCE=hive|trino`：自动路由使用的历史查询引擎。

假设当前业务日期为 `2026-08-06`，实时边界为 `2026-08-04`：

```text
请求 2026-07-01 至 2026-08-06
  |- Hive/Trino: 2026-07-01 至 2026-08-03
  `- StarRocks:  2026-08-04 至 2026-08-06
```

规则：

1. 请求完全早于实时边界：只查询配置的历史引擎。
2. 请求完全位于实时区：只查询 StarRocks。
3. 请求跨边界：产生两个不重叠分段，通过 `errgroup.WithContext` 并行执行。
4. 任一分段失败时取消同一请求的其他查询并返回整体错误。
5. 两段均成功后：
   - 订单数与六个阶段数分别使用 `int64` 相加，并检查溢出。
   - 销售额使用 `decimal.Decimal.Add`。
6. 响应 `meta.segments` 明确列出实际数据源和日期范围。

不做自动降级和盲目重试。只读 SQL 也可能是高成本操作，自动重试可能放大数据库压力；重试应由具备查询 ID 和幂等语义的后续策略单独设计。

## 7. HTTP API

所有业务端点要求：

```http
X-API-Key: <secret>
```

API Key 只从环境变量读取，使用常量时间比较，不写入日志。健康检查与 Prometheus 指标端点不要求 API Key，部署文档要求通过内部网络或网关限制访问。

### 7.1 零售汇总

```http
GET /api/v1/retail/summary
    ?start_date=2026-07-01
    &end_date=2026-08-06
    &source=auto
    &org_id=ORG-001
```

`org_id` 必填，长度 1 到 64，只允许 ASCII 字母、数字、下划线和连字符。

成功响应：

```json
{
  "data": {
    "order_count": 1280,
    "sales_amount": "3568200.50"
  },
  "meta": {
    "request_id": "01J...",
    "requested_source": "auto",
    "segments": [
      {
        "source": "hive",
        "start_date": "2026-07-01",
        "end_date": "2026-08-03"
      },
      {
        "source": "starrocks",
        "start_date": "2026-08-04",
        "end_date": "2026-08-06"
      }
    ]
  }
}
```

### 7.2 家装阶段汇总

```http
GET /api/v1/renovation/funnel
    ?start_date=2026-07-01
    &end_date=2026-08-06
    &source=auto
    &org_id=ORG-001
```

成功响应的 `data`：

```json
{
  "lead_count": 1000,
  "invited_count": 720,
  "measured_count": 460,
  "signed_count": 210,
  "started_count": 160,
  "completed_count": 120
}
```

响应中的 `meta` 与零售端点一致。

### 7.3 运行端点

- `GET /health/live`：只验证进程事件循环可响应。
- `GET /health/ready`：在真实模式下并行 Ping 当前启用的适配器；在内存模式下直接成功。
- `GET /metrics`：Prometheus 格式的请求量、耗时、错误数和数据源查询指标。

## 8. 错误模型

统一响应：

```json
{
  "error": {
    "code": "QUERY_TIMEOUT",
    "message": "query timed out",
    "request_id": "01J..."
  }
}
```

映射规则：

| HTTP 状态 | 错误码 | 场景 |
| --- | --- | --- |
| 400 | `INVALID_ARGUMENT` | 日期、来源或组织参数非法 |
| 401 | `UNAUTHORIZED` | API Key 缺失或错误 |
| 503 | `DEPENDENCY_UNAVAILABLE` | 当前数据适配器无法连接或执行 |
| 504 | `QUERY_TIMEOUT` | 请求 context 超时 |
| 500 | `INTERNAL_ERROR` | 未预期错误 |

外部响应不包含 SQL、DSN、数据库主机、用户名或底层驱动错误。完整错误链只进入服务端结构化日志，并携带 `request_id`、数据源和分段范围。

## 9. 数据表设计

Hive 与 StarRocks 使用相同逻辑列，便于路由服务合并。物理建表参数分别针对两个引擎优化。

### 9.1 零售日汇总

```text
dws_retail_order_daily
  stat_date      DATE
  org_id         STRING/VARCHAR
  order_count    BIGINT
  sales_amount   DECIMAL(18,2)
  updated_at     TIMESTAMP/DATETIME
```

Hive 按 `stat_date` 分区。StarRocks 使用适合按组织和日期查询的明细或聚合模型，并按 `stat_date` 分区。

### 9.2 家装阶段日汇总

```text
dws_renovation_stage_daily
  stat_date       DATE
  org_id          STRING/VARCHAR
  lead_count      BIGINT
  invited_count   BIGINT
  measured_count  BIGINT
  signed_count    BIGINT
  started_count   BIGINT
  completed_count BIGINT
  updated_at      TIMESTAMP/DATETIME
```

查询按 `org_id` 和闭区间日期过滤，并对指标求和。

### 9.3 SQL 安全

- StarRocks 使用 MySQL 驱动参数占位符。
- Trino 使用官方驱动提供的参数绑定。
- HiveServer2 驱动明确不支持查询参数，因此只允许经过领域层严格校验的值进入 SQL：
  - 日期必须先解析为 `time.Time`，再由程序格式化为固定 `YYYY-MM-DD`。
  - `org_id` 必须匹配 `^[A-Za-z0-9_-]{1,64}$`。
  - 库、Schema 和表名只来自启动配置，并匹配 `^[A-Za-z_][A-Za-z0-9_]*$`。
  - Hive SQL 构造集中在一个专用构建器中，不接受自由字符串片段。

## 10. 配置与运行模式

核心环境变量：

```text
APP_ENV=development
APP_ADDR=:8080
APP_TIMEZONE=Asia/Shanghai
API_KEY=change-me
QUERY_TIMEOUT=10s
SHUTDOWN_TIMEOUT=15s
MAX_QUERY_DAYS=366
REALTIME_WINDOW_DAYS=3
DATA_MODE=memory|real
HISTORICAL_SOURCE=hive|trino
```

真实连接分别使用 `HIVE_*`、`TRINO_*`、`STARROCKS_*` 前缀，包括地址、数据库、用户名、密码、TLS CA 路径和连接池配置。密码不会出现在默认配置文件、日志或错误响应中。

用户名/密码与 TLS 在本版范围内。Kerberos 仅在文档中说明扩展点，不实现 Keytab、票据续期或 GSSAPI 运维流程。

## 11. 可观测性与生命周期

### 11.1 日志

使用 `slog` 输出 JSON，业务请求包含：

- `request_id`
- HTTP 方法和路由模板
- 状态码与耗时
- 请求来源和实际数据源
- 查询日期范围
- 错误类别

不记录 API Key、数据库密码和完整 DSN。

### 11.2 指标

至少包括：

- `http_requests_total{route,method,status}`
- `http_request_duration_seconds{route,method}`
- `data_query_total{source,metric,status}`
- `data_query_duration_seconds{source,metric}`

标签不得包含 `org_id`、`request_id` 或原始 URL，避免高基数。

### 11.3 连接池和停机

- 为每个 `sql.DB` 配置最大打开连接、最大空闲连接、连接最大生命周期和空闲时间。
- 启动时构造连接池并执行有超时的 Ping；配置错误立即终止启动。
- 接收 `SIGINT` 或 `SIGTERM` 后停止接受新请求，在 `SHUTDOWN_TIMEOUT` 内等待在途请求，然后关闭数据库连接。

## 12. 测试设计

实施遵循测试先行。

### 12.1 领域和服务单元测试

- 日期解析、反向日期、跨度超限。
- 3 日边界计算。
- 只查历史、只查实时、跨区分段。
- 显式 `hive`、`trino`、`starrocks` 不分段。
- 并行分段成功合并。
- 任一分段失败时整体失败并取消另一个 context。
- 金额精确相加。
- 整数溢出返回内部错误。

时钟通过接口注入，测试不依赖真实当天日期。

### 12.2 适配器测试

- `sqlmock` 验证 StarRocks 与 Trino SQL、参数和结果扫描。
- Hive 专用测试验证严格校验后的 SQL 文本，不允许未经校验的输入。
- 覆盖空结果、NULL、非法金额、扫描失败和驱动错误包装。
- 内存适配器使用真实聚合逻辑测试日期与组织过滤。

### 12.3 HTTP 集成测试

使用 `httptest` 覆盖：

- API Key 缺失、错误和正确。
- 成功响应与响应头中的 Request ID。
- 所有参数验证错误。
- 503、504 和 500 错误映射。
- liveness、readiness 和 metrics。
- panic recovery 不泄露内部信息。

### 12.4 真实环境冒烟测试

使用独立 build tag 和环境变量，按需连接 HiveServer2、Trino 与 StarRocks，执行最小聚合查询。默认 CI 和 `make test` 不依赖外部集群。

## 13. 项目交付

交付内容：

- 可运行的 Go 服务和完整中文学习注释。
- 内存演示模式及相对当前日期生成的确定性家装行业示例数据。
- Hive、Trino、StarRocks 真实适配器。
- Hive 与 StarRocks DDL、示例数据 SQL。
- OpenAPI 3.1 文档与 curl 调用示例。
- `.env.example`，不包含真实密钥。
- 多阶段、非 root Dockerfile。
- Makefile：`fmt`、`lint`、`test`、`run`、`build`。
- README：从 Go 项目结构、配置、启动、测试到真实数据源切换的完整流程。

不在本次范围：

- 任意 SQL 查询接口。
- Kerberos。
- 分布式缓存。
- Cohort 转化率。
- 多租户授权模型。
- 完整 Hive、Trino、StarRocks Docker 集群编排。
- 微服务拆分。

## 14. 验收标准

1. `DATA_MODE=memory` 时无需外部服务即可启动，两个业务 API 均返回符合口径的数据。
2. `source=auto` 能根据固定时钟正确生成一个或两个查询分段，并在响应中披露分段。
3. HiveServer2、Trino、StarRocks 适配器均可通过配置装配，凭据不进入日志。
4. API Key、超时、错误映射、健康检查、指标和优雅停机均有自动化测试。
5. 全部单元与 HTTP 集成测试通过，格式化与静态检查通过。
6. Docker 镜像以非 root 用户运行。
7. README 能让有其他后端经验的开发者从零完成启动、调用、测试和切换真实数据源。
