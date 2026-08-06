package database

import (
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/beltran/gohive/v2"
	"github.com/go-sql-driver/mysql"
	"github.com/trinodb/trino-go-client/trino"

	"github.com/woo721/cursor_test/internal/config"
)

// 可注入的 opener / 注册钩子，便于单测断言构造参数且不打开真实网络套接字。
var (
	hiveOpener = func(cfg gohive.Config) *sql.DB {
		return gohive.OpenDB(cfg)
	}
	trinoOpener = func(dsn string) (*sql.DB, error) {
		return sql.Open("trino", dsn)
	}
	trinoRegisterClient = trino.RegisterCustomClient
	mysqlNewConnector   = mysql.NewConnector
	mysqlRegisterTLS    = mysql.RegisterTLSConfig
	configurePoolFn     = ConfigurePool
	tlsNameSeq          uint64
)

// OpenHive 通过 gohive v2 构造 *sql.DB，不在此处 Ping；启动探活由 bootstrap 统一超时控制。
func OpenHive(cfg config.Hive) (*sql.DB, error) {
	var tlsCfg *tls.Config
	var err error
	if cfg.TLS.Enabled {
		tlsCfg, err = loadTLS(cfg.TLS.CAFile, cfg.TLS.CertFile, cfg.TLS.KeyFile, cfg.TLS.ServerName)
		if err != nil {
			return nil, fmt.Errorf("hive tls: %w", err)
		}
	}
	db := hiveOpener(gohive.Config{
		Host:          cfg.Host,
		Port:          cfg.Port,
		Auth:          cfg.Auth,
		Username:      cfg.Username,
		Password:      cfg.Password,
		Database:      cfg.Database,
		TransportMode: cfg.TransportMode,
		TLSConfig:     tlsCfg,
	})
	configurePoolFn(db, cfg.Pool)
	return db, nil
}

// OpenTrino 组装带转义用户信息的 DSN，并通过 custom_client 注入 TLS，避免依赖 InsecureSkipVerify。
func OpenTrino(cfg config.Trino) (*sql.DB, error) {
	scheme := "http"
	query := url.Values{}
	query.Set("catalog", cfg.Catalog)
	query.Set("schema", cfg.Schema)
	query.Set("source", cfg.Source)
	if cfg.QueryTimeout > 0 {
		query.Set("query_timeout", cfg.QueryTimeout.String())
	}

	if cfg.TLS.Enabled {
		scheme = "https"
		tlsCfg, err := loadTLS(cfg.TLS.CAFile, cfg.TLS.CertFile, cfg.TLS.KeyFile, cfg.TLS.ServerName)
		if err != nil {
			return nil, fmt.Errorf("trino tls: %w", err)
		}
		clientName := nextTLSName("trino")
		client := &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				TLSHandshakeTimeout: 10 * time.Second,
				TLSClientConfig:     tlsCfg,
			},
		}
		if err := trinoRegisterClient(clientName, client); err != nil {
			return nil, fmt.Errorf("trino register tls client: %w", err)
		}
		query.Set("custom_client", clientName)
	}

	u := &url.URL{
		Scheme:   scheme,
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		RawQuery: query.Encode(),
	}
	if cfg.Password != "" {
		u.User = url.UserPassword(cfg.User, cfg.Password)
	} else if cfg.User != "" {
		u.User = url.User(cfg.User)
	}

	db, err := trinoOpener(u.String())
	if err != nil {
		return nil, err
	}
	configurePoolFn(db, cfg.Pool)
	return db, nil
}

// OpenStarRocks 使用 mysql.Config（禁止手写 DSN 拼接）并 RegisterTLSConfig，再 sql.OpenDB。
func OpenStarRocks(cfg config.StarRocks) (*sql.DB, error) {
	mc := mysql.NewConfig()
	mc.User = cfg.Username
	mc.Passwd = cfg.Password
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	mc.DBName = cfg.Database
	mc.ParseTime = true
	mc.Timeout = cfg.DialTimeout
	mc.ReadTimeout = cfg.ReadTimeout
	mc.WriteTimeout = cfg.WriteTimeout

	if cfg.TLS.Enabled {
		tlsCfg, err := loadTLS(cfg.TLS.CAFile, cfg.TLS.CertFile, cfg.TLS.KeyFile, cfg.TLS.ServerName)
		if err != nil {
			return nil, fmt.Errorf("starrocks tls: %w", err)
		}
		name := nextTLSName("starrocks")
		if err := mysqlRegisterTLS(name, tlsCfg); err != nil {
			return nil, fmt.Errorf("starrocks register tls: %w", err)
		}
		mc.TLSConfig = name
	}

	connector, err := mysqlNewConnector(mc)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	configurePoolFn(db, cfg.Pool)
	return db, nil
}

// ConfigurePool 限制打开/空闲连接与生命周期，防止历史批查询拖垮实时链路的会话配额。
func ConfigurePool(db *sql.DB, pool config.Pool) {
	db.SetMaxOpenConns(pool.MaxOpen)
	db.SetMaxIdleConns(pool.MaxIdle)
	db.SetConnMaxLifetime(pool.MaxLifetime)
	db.SetConnMaxIdleTime(pool.MaxIdleTime)
}

// loadTLS 构造最低 TLS 1.2 的客户端配置；永不开启 InsecureSkipVerify，可选加载 mTLS 证书对。
func loadTLS(caFile, certFile, keyFile, serverName string) (*tls.Config, error) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: serverName,
	}
	if caFile != "" {
		pemBytes, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("failed to parse CA file %q", caFile)
		}
		tlsCfg.RootCAs = pool
	}
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, fmt.Errorf("cert and key must both be provided for mTLS")
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return tlsCfg, nil
}

func nextTLSName(prefix string) string {
	n := atomic.AddUint64(&tlsNameSeq, 1)
	return fmt.Sprintf("%s-tls-%d", prefix, n)
}

// --- test hooks ---

func SetHiveOpenerForTest(fn func(gohive.Config) *sql.DB) func() {
	prev := hiveOpener
	hiveOpener = fn
	return func() { hiveOpener = prev }
}

func SetTrinoOpenerForTest(fn func(string) (*sql.DB, error)) func() {
	prev := trinoOpener
	trinoOpener = fn
	return func() { trinoOpener = prev }
}

func SetTrinoRegisterClientForTest(fn func(string, *http.Client) error) func() {
	prev := trinoRegisterClient
	trinoRegisterClient = fn
	return func() { trinoRegisterClient = prev }
}

func SetStarRocksConnectorForTest(fn func(*mysql.Config) (driver.Connector, error)) func() {
	prev := mysqlNewConnector
	mysqlNewConnector = fn
	return func() { mysqlNewConnector = prev }
}

func SetMySQLRegisterTLSForTest(fn func(string, *tls.Config) error) func() {
	prev := mysqlRegisterTLS
	mysqlRegisterTLS = fn
	return func() { mysqlRegisterTLS = prev }
}

func SetConfigurePoolForTest(fn func(*sql.DB, config.Pool)) func() {
	prev := configurePoolFn
	configurePoolFn = fn
	return func() { configurePoolFn = prev }
}
