# 家装数据查询 API

## 1. 业务故事：为什么拆分 Hive/StarRocks

这个服务面向家装业务的只读报表查询：零售订单汇总回答“某组织在一段日期内有多少订单、销售额是多少”，家装漏斗回答“线索、邀约、量房、签约、开工、完工各阶段有多少”。接口统一返回按组织和自然日汇总后的结果，调用方不需要理解底层引擎。

存储分层的约定是：Hive 或 Trino-to-Hive 保存稳定历史数据，StarRocks 保存近实时数据。`source=auto` 会按 `REALTIME_WINDOW_DAYS` 计算实时 cutoff，把历史段发往 `HISTORICAL_SOURCE`，把实时段发往 StarRocks，并在全部分段成功后再合并，避免重复计算和半截数据。

## 2. Go 包与请求流向图

主要包：

- `cmd/server`：解析配置、构建应用、启动 HTTP server、处理优雅关闭。
- `internal/config`：把环境变量解析成强类型配置，并校验端口、时长、表名、TLS 文件。
- `internal/bootstrap`：按 `DATA_MODE` 组装内存仓库或真实 Hive/Trino/StarRocks 仓库，注入指标和 HTTP handler。
- `internal/httpapi`：路由、API Key 中间件、超时、request_id、错误映射和 JSON DTO。
- `internal/app`：查询编排、`source=auto` 分段、并发查询和结果合并。
- `internal/domain`：数据源枚举、日期范围、组织 ID 校验和领域模型。
- `internal/adapter/memory`：本地演示数据。
- `internal/adapter/sqlrepo`：固定 SQL 构造与 `database/sql` 查询映射。
- `internal/database`：HiveServer2、Trino、StarRocks 连接打开和 TLS 配置。
- `internal/observability`：Prometheus 指标。

请求流向：

```text
HTTP request
  -> internal/httpapi middleware
  -> internal/httpapi handler parses source/org_id/start_date/end_date
  -> internal/app QueryService plans segments
  -> internal/adapter/sqlrepo or memory repository
  -> Hive, Trino, StarRocks, or deterministic memory data
  -> merged JSON response with data and meta.segments
```

## 3. 前置条件与 toolchain 自动下载

需要 Go，建议保持 `GOTOOLCHAIN=auto`。`go.mod` 声明 `go 1.26.0` 和 `toolchain go1.26.5`，支持自动下载匹配工具链的 Go 版本。常用工具：

- `make`
- `go`
- `npx`，仅用于 OpenAPI lint
- `docker`，仅用于本地镜像构建；当前云环境可能没有安装

真实数据源模式还需要已经可访问的 HiveServer2、Trino 和 StarRocks。仓库不提供这些外部集群的 Docker Compose 编排。

## 4. `make run` 内存模式演示

本地最快启动方式：

```bash
make run
```

这个目标会使用默认 `API_KEY=local-development-key` 和 `DATA_MODE=memory`，不会连接外部网络服务。内存仓库按 `org_id` 和日期生成确定性数据，适合学习接口、调试路由和跑通 curl。

## 5. 两个 API 与各 source 的 curl 示例

业务接口都需要 `X-API-Key`：

```bash
export API_KEY=local-development-key
export BASE_URL=http://localhost:8080
```

零售订单汇总，覆盖 `auto`、`hive`、`trino`、`starrocks`：

```bash
for source in auto hive trino starrocks; do
  curl -sS -H "X-API-Key: ${API_KEY}" \
    "${BASE_URL}/api/v1/retail/summary?source=${source}&org_id=ORG-001&start_date=2026-08-01&end_date=2026-08-06"
  echo
done
```

家装漏斗，覆盖 `auto`、`hive`、`trino`、`starrocks`：

```bash
for source in auto hive trino starrocks; do
  curl -sS -H "X-API-Key: ${API_KEY}" \
    "${BASE_URL}/api/v1/renovation/funnel?source=${source}&org_id=ORG-001&start_date=2026-08-01&end_date=2026-08-06"
  echo
done
```

运维端点不需要 API Key：

```bash
curl -i "${BASE_URL}/health/live"
curl -i "${BASE_URL}/health/ready"
curl -sS "${BASE_URL}/metrics" | head
```

## 6. 阅读路由、接口、适配器、中间件与启动代码

建议按这个顺序读：

1. `internal/httpapi/handler.go`：确认公开路由、查询参数、响应字段和 `meta.segments`。
2. `internal/domain/model.go`：看 `Source` 枚举、`org_id` 正则、日期范围闭区间和最大天数限制。
3. `internal/app/query_service.go`：理解 `source=auto` 如何按 cutoff 切分、并发执行、失败时拒绝部分数据。
4. `internal/adapter/sqlrepo/statements.go`：看 Hive 字面量安全校验，以及 Trino/StarRocks 参数绑定 SQL。
5. `internal/httpapi/middleware.go`：看 API Key、request_id、超时、访问日志和公开路径。
6. `internal/bootstrap/app.go`：看 `DATA_MODE=memory` 与 `DATA_MODE=real` 的装配差异。
7. `cmd/server/main.go`：看配置加载、监听地址和优雅关闭。

## 7. 测试命令与 TDD 如何塑造接口

常用测试：

```bash
make fmt
make vet
make test
make test-race
```

真实数据源冒烟测试是 opt-in：

```bash
go test -tags=integration ./tests/smoke
```

测试先固定了窄接口：HTTP 层只依赖 `QueryService` 和 `Readiness`，SQL 仓库只依赖最小 `DB` 端口，数据库打开函数可替换。这样单元测试可以覆盖参数校验、错误映射、SQL 构造、TLS 配置和查询编排，而不需要真实集群。

## 8. HiveServer2 用户名、密码与 TLS 设置

真实模式需要设置 Hive 连接信息：

```bash
export DATA_MODE=real
export HIVE_HOST=hive.example.invalid
export HIVE_PORT=10000
export HIVE_USERNAME=example_hive_user
export HIVE_PASSWORD=example_hive_password
export HIVE_DATABASE=analytics
export HIVE_AUTH=NONE
export HIVE_TRANSPORT_MODE=binary
```

启用 TLS 时：

```bash
export HIVE_TLS_ENABLED=true
export HIVE_TLS_CA_FILE=/path/to/example-ca.pem
export HIVE_TLS_SERVER_NAME=hive.example.invalid
```

如果需要 mTLS，`HIVE_TLS_CERT_FILE` 和 `HIVE_TLS_KEY_FILE` 必须同时设置。示例路径是占位符，真实部署应使用密钥管理系统或挂载的 Secret。

## 9. Trino-to-Hive 设置

Trino 用于查询 Hive 历史表时，需要 catalog、schema 和用户：

```bash
export TRINO_HOST=trino.example.invalid
export TRINO_PORT=8080
export TRINO_USER=example_trino_user
export TRINO_PASSWORD=example_trino_password
export TRINO_CATALOG=hive
export TRINO_SCHEMA=analytics
export TRINO_SOURCE=home-improvement-query-api
export TRINO_QUERY_TIMEOUT=10s
```

TLS 变量与 Hive 同形：`TRINO_TLS_ENABLED`、`TRINO_TLS_CA_FILE`、`TRINO_TLS_CERT_FILE`、`TRINO_TLS_KEY_FILE`、`TRINO_TLS_SERVER_NAME`。服务端证书校验不会关闭。

## 10. StarRocks 设置

StarRocks 通过 MySQL 协议访问：

```bash
export STARROCKS_HOST=starrocks.example.invalid
export STARROCKS_PORT=9030
export STARROCKS_USERNAME=example_starrocks_user
export STARROCKS_PASSWORD=example_starrocks_password
export STARROCKS_DATABASE=analytics
export STARROCKS_DIAL_TIMEOUT=10s
export STARROCKS_READ_TIMEOUT=10s
export STARROCKS_WRITE_TIMEOUT=10s
```

TLS 变量为 `STARROCKS_TLS_ENABLED`、`STARROCKS_TLS_CA_FILE`、`STARROCKS_TLS_CERT_FILE`、`STARROCKS_TLS_KEY_FILE`、`STARROCKS_TLS_SERVER_NAME`。连接池变量为 `STARROCKS_MAX_OPEN_CONNS`、`STARROCKS_MAX_IDLE_CONNS`、`STARROCKS_CONN_MAX_LIFETIME`、`STARROCKS_CONN_MAX_IDLE_TIME`。

## 11. DDL 加载与 cutoff/non-overlap 合约

DDL 位于：

- `deploy/hive/schema.sql`
- `deploy/starrocks/schema.sql`

Hive 表按 `stat_date` 分区，StarRocks 表按 `stat_date` RANGE 分区，并用 `(org_id, stat_date)` 主键服务组织和日期聚合查询。两边的度量列均为非空；计数列是 `BIGINT`，销售额是 `DECIMAL(18,2)`。

加载数据前先确定实时 cutoff。默认 `REALTIME_WINDOW_DAYS=3` 时，今天、昨天、前天属于 StarRocks；更早日期属于 Hive 或 Trino-to-Hive。不要把同一个 `org_id/stat_date` 同时放入历史表和实时表，否则 `source=auto` 会重复计数。示例 DDL 使用 `ORG-001` 演示历史日期和近期日期的写入方式。

## 12. 生产部署检查清单

- 使用长随机 `API_KEY`，通过 Secret 注入，不写入镜像或仓库。
- 设置 `APP_TIMEZONE`，保证业务自然日与报表口径一致。
- 根据下游 SLA 调整 `QUERY_TIMEOUT`、连接池上限和 StarRocks 读写超时。
- 明确 `HISTORICAL_SOURCE=hive` 或 `trino`，并设置 `REALTIME_WINDOW_DAYS`。
- 先加载 DDL，再启用 `DATA_MODE=real`。
- 确认 `/health/ready` 会 Ping 所有真实数据源。
- 把 `/metrics` 接入 Prometheus，并按 source、metric、status 建告警。
- 镜像运行用户保持 non-root，容器只暴露 8080。
- 不把真实密码、证书私钥、Kerberos keytab 写入日志、镜像层或 Git。

## 13. Kerberos 与 Cohort 转化率扩展点

Kerberos：当前配置已经保留 Hive `HIVE_AUTH` 字段，后续可在 `internal/database.OpenHive` 中扩展 Kerberos 认证参数，例如 principal、realm、keytab、ccache 路径。配置层应继续做强类型校验，日志层继续只输出脱敏元数据。

Cohort 转化率：领域层可以增加 Cohort 查询模型，应用层沿用“计划分段、并发查询、全部成功后合并”的模式。SQL 适配器应新增固定形状的 cohort statement，不接受任意 SQL 片段；HTTP 层新增 DTO 时继续保持 `data` 和 `meta` 包装，并在 OpenAPI 中同步 schema。
