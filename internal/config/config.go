package config

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config 是进程启动时解析出的强类型运行配置。
type Config struct {
	Environment string
	Server      Server
	Query       Query
	DataMode    string
	APIKey      string
	Tables      Tables
	Hive        Hive
	Trino       Trino
	StarRocks   StarRocks
}

type Server struct {
	Addr            string
	Timezone        *time.Location
	ShutdownTimeout time.Duration
}

type Query struct {
	Timeout            time.Duration
	MaxQueryDays       int
	RealtimeWindowDays int
	HistoricalSource   string
}

type Tables struct {
	Prefix     string
	Retail     string
	Renovation string
}

// Pool 控制 database/sql 连接池上限，避免慢查询占满进程文件描述符与后端会话。
type Pool struct {
	MaxOpen     int
	MaxIdle     int
	MaxLifetime time.Duration
	MaxIdleTime time.Duration
}

// TLSFiles 描述可选的服务端校验与 mTLS 材料路径；密码与密钥仅驻留进程内存。
type TLSFiles struct {
	Enabled    bool
	CAFile     string
	CertFile   string
	KeyFile    string
	ServerName string
}

type Hive struct {
	Host          string
	Port          int
	Username      string
	Password      string
	Database      string
	Auth          string
	TransportMode string
	TLS           TLSFiles
	Pool          Pool
}

type Trino struct {
	Host         string
	Port         int
	User         string
	Password     string
	Catalog      string
	Schema       string
	Source       string
	QueryTimeout time.Duration
	TLS          TLSFiles
	Pool         Pool
}

type StarRocks struct {
	Host         string
	Port         int
	Username     string
	Password     string
	Database     string
	DialTimeout  time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	TLS          TLSFiles
	Pool         Pool
}

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Load 从 getenv 解析配置；API_KEY 始终必填，DATA_MODE=real 时要求三源均可显式查询。
func Load(getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}

	apiKey, err := required(getenv, "API_KEY")
	if err != nil {
		return Config{}, err
	}

	tzName := stringDefault(getenv, "APP_TIMEZONE", "Asia/Shanghai")
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return Config{}, fmt.Errorf("APP_TIMEZONE: %w", err)
	}

	queryTimeout, err := durationDefault(getenv, "QUERY_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := durationDefault(getenv, "SHUTDOWN_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	maxDays, err := intDefault(getenv, "MAX_QUERY_DAYS", 366)
	if err != nil {
		return Config{}, err
	}
	if maxDays < 1 {
		return Config{}, fmt.Errorf("MAX_QUERY_DAYS: must be >= 1")
	}
	realtimeDays, err := intDefault(getenv, "REALTIME_WINDOW_DAYS", 3)
	if err != nil {
		return Config{}, err
	}
	if realtimeDays < 1 {
		return Config{}, fmt.Errorf("REALTIME_WINDOW_DAYS: must be >= 1")
	}

	historical := stringDefault(getenv, "HISTORICAL_SOURCE", "hive")
	switch historical {
	case "hive", "trino":
	default:
		return Config{}, fmt.Errorf("HISTORICAL_SOURCE: must be hive or trino")
	}

	dataMode := stringDefault(getenv, "DATA_MODE", "memory")
	switch dataMode {
	case "memory", "real":
	default:
		return Config{}, fmt.Errorf("DATA_MODE: must be memory or real")
	}

	tables := Tables{
		Prefix:     stringDefault(getenv, "TABLE_PREFIX", "analytics"),
		Retail:     stringDefault(getenv, "TABLE_RETAIL", "dws_retail_order_daily"),
		Renovation: stringDefault(getenv, "TABLE_RENOVATION", "dws_renovation_stage_daily"),
	}
	for _, pair := range []struct {
		name, value string
	}{
		{"TABLE_PREFIX", tables.Prefix},
		{"TABLE_RETAIL", tables.Retail},
		{"TABLE_RENOVATION", tables.Renovation},
	} {
		if !identifierPattern.MatchString(pair.value) {
			return Config{}, fmt.Errorf("%s: invalid table identifier %q", pair.name, pair.value)
		}
	}

	defaultPool := Pool{
		MaxOpen:     10,
		MaxIdle:     5,
		MaxLifetime: 30 * time.Minute,
		MaxIdleTime: 5 * time.Minute,
	}

	hive, err := loadHive(getenv, defaultPool)
	if err != nil {
		return Config{}, err
	}
	trino, err := loadTrino(getenv, defaultPool, queryTimeout)
	if err != nil {
		return Config{}, err
	}
	starrocks, err := loadStarRocks(getenv, defaultPool)
	if err != nil {
		return Config{}, err
	}

	if dataMode == "real" {
		if hive.Host == "" || hive.Database == "" {
			return Config{}, fmt.Errorf("DATA_MODE=real requires HIVE_HOST and HIVE_DATABASE")
		}
		if trino.Host == "" || trino.Catalog == "" || trino.Schema == "" || trino.User == "" {
			return Config{}, fmt.Errorf("DATA_MODE=real requires TRINO_HOST, TRINO_USER, TRINO_CATALOG and TRINO_SCHEMA")
		}
		if starrocks.Host == "" || starrocks.Database == "" || starrocks.Username == "" {
			return Config{}, fmt.Errorf("DATA_MODE=real requires STARROCKS_HOST, STARROCKS_USERNAME and STARROCKS_DATABASE")
		}
	}

	return Config{
		Environment: stringDefault(getenv, "APP_ENV", "development"),
		Server: Server{
			Addr:            stringDefault(getenv, "APP_ADDR", ":8080"),
			Timezone:        loc,
			ShutdownTimeout: shutdownTimeout,
		},
		Query: Query{
			Timeout:            queryTimeout,
			MaxQueryDays:       maxDays,
			RealtimeWindowDays: realtimeDays,
			HistoricalSource:   historical,
		},
		DataMode:  dataMode,
		APIKey:    apiKey,
		Tables:    tables,
		Hive:      hive,
		Trino:     trino,
		StarRocks: starrocks,
	}, nil
}

// LogValue 实现 slog.LogValuer：日志中只保留结构元数据，密码与 API Key 一律替换为 [REDACTED]，
// 避免 JSON handler 反射打印结构体时把明文密钥写入集中日志。
func (c Config) LogValue() slog.Value {
	return slog.AnyValue(map[string]any{
		"environment": c.Environment,
		"server": map[string]any{
			"addr":             c.Server.Addr,
			"timezone":         c.Server.Timezone.String(),
			"shutdown_timeout": c.Server.ShutdownTimeout.String(),
		},
		"query": map[string]any{
			"timeout":              c.Query.Timeout.String(),
			"max_query_days":       c.Query.MaxQueryDays,
			"realtime_window_days": c.Query.RealtimeWindowDays,
			"historical_source":    c.Query.HistoricalSource,
		},
		"data_mode": c.DataMode,
		"api_key":   "[REDACTED]",
		"tables": map[string]any{
			"prefix":     c.Tables.Prefix,
			"retail":     c.Tables.Retail,
			"renovation": c.Tables.Renovation,
		},
		"hive": map[string]any{
			"host":           c.Hive.Host,
			"port":           c.Hive.Port,
			"username":       c.Hive.Username,
			"password":       "[REDACTED]",
			"database":       c.Hive.Database,
			"auth":           c.Hive.Auth,
			"transport_mode": c.Hive.TransportMode,
			"tls":            tlsLog(c.Hive.TLS),
			"pool":           poolLog(c.Hive.Pool),
		},
		"trino": map[string]any{
			"host":          c.Trino.Host,
			"port":          c.Trino.Port,
			"user":          c.Trino.User,
			"password":      "[REDACTED]",
			"catalog":       c.Trino.Catalog,
			"schema":        c.Trino.Schema,
			"source":        c.Trino.Source,
			"query_timeout": c.Trino.QueryTimeout.String(),
			"tls":           tlsLog(c.Trino.TLS),
			"pool":          poolLog(c.Trino.Pool),
		},
		"starrocks": map[string]any{
			"host":          c.StarRocks.Host,
			"port":          c.StarRocks.Port,
			"username":      c.StarRocks.Username,
			"password":      "[REDACTED]",
			"database":      c.StarRocks.Database,
			"dial_timeout":  c.StarRocks.DialTimeout.String(),
			"read_timeout":  c.StarRocks.ReadTimeout.String(),
			"write_timeout": c.StarRocks.WriteTimeout.String(),
			"tls":           tlsLog(c.StarRocks.TLS),
			"pool":          poolLog(c.StarRocks.Pool),
		},
	})
}

func tlsLog(t TLSFiles) map[string]any {
	return map[string]any{
		"enabled":     t.Enabled,
		"ca_file":     t.CAFile,
		"cert_file":   t.CertFile,
		"key_file":    t.KeyFile,
		"server_name": t.ServerName,
	}
}

func poolLog(p Pool) map[string]any {
	return map[string]any{
		"max_open":      p.MaxOpen,
		"max_idle":      p.MaxIdle,
		"max_lifetime":  p.MaxLifetime.String(),
		"max_idle_time": p.MaxIdleTime.String(),
	}
}

func loadHive(getenv func(string) string, defaults Pool) (Hive, error) {
	port, err := portDefault(getenv, "HIVE_PORT", 10000)
	if err != nil {
		return Hive{}, err
	}
	tls, err := loadTLSFiles(getenv, "HIVE_TLS")
	if err != nil {
		return Hive{}, err
	}
	pool, err := loadPool(getenv, "HIVE", defaults)
	if err != nil {
		return Hive{}, err
	}
	return Hive{
		Host:          getenv("HIVE_HOST"),
		Port:          port,
		Username:      getenv("HIVE_USERNAME"),
		Password:      getenv("HIVE_PASSWORD"),
		Database:      getenv("HIVE_DATABASE"),
		Auth:          stringDefault(getenv, "HIVE_AUTH", "NONE"),
		TransportMode: stringDefault(getenv, "HIVE_TRANSPORT_MODE", "binary"),
		TLS:           tls,
		Pool:          pool,
	}, nil
}

func loadTrino(getenv func(string) string, defaults Pool, queryTimeout time.Duration) (Trino, error) {
	port, err := portDefault(getenv, "TRINO_PORT", 8080)
	if err != nil {
		return Trino{}, err
	}
	tls, err := loadTLSFiles(getenv, "TRINO_TLS")
	if err != nil {
		return Trino{}, err
	}
	pool, err := loadPool(getenv, "TRINO", defaults)
	if err != nil {
		return Trino{}, err
	}
	qt, err := durationDefault(getenv, "TRINO_QUERY_TIMEOUT", queryTimeout)
	if err != nil {
		return Trino{}, err
	}
	return Trino{
		Host:         getenv("TRINO_HOST"),
		Port:         port,
		User:         getenv("TRINO_USER"),
		Password:     getenv("TRINO_PASSWORD"),
		Catalog:      getenv("TRINO_CATALOG"),
		Schema:       getenv("TRINO_SCHEMA"),
		Source:       stringDefault(getenv, "TRINO_SOURCE", "home-improvement-query-api"),
		QueryTimeout: qt,
		TLS:          tls,
		Pool:         pool,
	}, nil
}

func loadStarRocks(getenv func(string) string, defaults Pool) (StarRocks, error) {
	port, err := portDefault(getenv, "STARROCKS_PORT", 9030)
	if err != nil {
		return StarRocks{}, err
	}
	tls, err := loadTLSFiles(getenv, "STARROCKS_TLS")
	if err != nil {
		return StarRocks{}, err
	}
	pool, err := loadPool(getenv, "STARROCKS", defaults)
	if err != nil {
		return StarRocks{}, err
	}
	dialTimeout, err := durationDefault(getenv, "STARROCKS_DIAL_TIMEOUT", 10*time.Second)
	if err != nil {
		return StarRocks{}, err
	}
	readTimeout, err := durationDefault(getenv, "STARROCKS_READ_TIMEOUT", 10*time.Second)
	if err != nil {
		return StarRocks{}, err
	}
	writeTimeout, err := durationDefault(getenv, "STARROCKS_WRITE_TIMEOUT", 10*time.Second)
	if err != nil {
		return StarRocks{}, err
	}
	return StarRocks{
		Host:         getenv("STARROCKS_HOST"),
		Port:         port,
		Username:     getenv("STARROCKS_USERNAME"),
		Password:     getenv("STARROCKS_PASSWORD"),
		Database:     getenv("STARROCKS_DATABASE"),
		DialTimeout:  dialTimeout,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		TLS:          tls,
		Pool:         pool,
	}, nil
}

func loadPool(getenv func(string) string, prefix string, defaults Pool) (Pool, error) {
	maxOpen, err := intDefault(getenv, prefix+"_MAX_OPEN_CONNS", defaults.MaxOpen)
	if err != nil {
		return Pool{}, err
	}
	maxIdle, err := intDefault(getenv, prefix+"_MAX_IDLE_CONNS", defaults.MaxIdle)
	if err != nil {
		return Pool{}, err
	}
	lifetime, err := durationDefault(getenv, prefix+"_CONN_MAX_LIFETIME", defaults.MaxLifetime)
	if err != nil {
		return Pool{}, err
	}
	idleTime, err := durationDefault(getenv, prefix+"_CONN_MAX_IDLE_TIME", defaults.MaxIdleTime)
	if err != nil {
		return Pool{}, err
	}
	if maxOpen < 1 {
		return Pool{}, fmt.Errorf("%s_MAX_OPEN_CONNS: must be >= 1", prefix)
	}
	if maxIdle < 0 {
		return Pool{}, fmt.Errorf("%s_MAX_IDLE_CONNS: must be >= 0", prefix)
	}
	return Pool{
		MaxOpen:     maxOpen,
		MaxIdle:     maxIdle,
		MaxLifetime: lifetime,
		MaxIdleTime: idleTime,
	}, nil
}

func loadTLSFiles(getenv func(string) string, prefix string) (TLSFiles, error) {
	enabled, err := boolDefault(getenv, prefix+"_ENABLED", false)
	if err != nil {
		return TLSFiles{}, err
	}
	tls := TLSFiles{
		Enabled:    enabled,
		CAFile:     getenv(prefix + "_CA_FILE"),
		CertFile:   getenv(prefix + "_CERT_FILE"),
		KeyFile:    getenv(prefix + "_KEY_FILE"),
		ServerName: getenv(prefix + "_SERVER_NAME"),
	}
	if !tls.Enabled && tls.CAFile == "" && tls.CertFile == "" && tls.KeyFile == "" {
		return tls, nil
	}
	if (tls.CertFile == "") != (tls.KeyFile == "") {
		return TLSFiles{}, fmt.Errorf("%s: cert and key must both be set for mTLS", prefix)
	}
	for _, pair := range []struct {
		label, path string
	}{
		{"CA_FILE", tls.CAFile},
		{"CERT_FILE", tls.CertFile},
		{"KEY_FILE", tls.KeyFile},
	} {
		if pair.path == "" {
			continue
		}
		if _, err := os.Stat(pair.path); err != nil {
			return TLSFiles{}, fmt.Errorf("%s_%s: %w", prefix, pair.label, err)
		}
	}
	return tls, nil
}

func required(getenv func(string) string, key string) (string, error) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return "", fmt.Errorf("%s: required", key)
	}
	return v, nil
}

func stringDefault(getenv func(string) string, key, def string) string {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def
	}
	return v
}

func intDefault(getenv func(string) string, key string, def int) (int, error) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func durationDefault(getenv func(string) string, key string, def time.Duration) (time.Duration, error) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: must be > 0", key)
	}
	return d, nil
}

func boolDefault(getenv func(string) string, key string, def bool) (bool, error) {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}

func portDefault(getenv func(string) string, key string, def int) (int, error) {
	n, err := intDefault(getenv, key, def)
	if err != nil {
		return 0, err
	}
	if n < 1 || n > 65535 {
		return 0, fmt.Errorf("%s: port must be in 1..65535", key)
	}
	return n, nil
}
