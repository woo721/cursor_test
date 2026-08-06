package database_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"database/sql/driver"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/beltran/gohive/v2"
	"github.com/go-sql-driver/mysql"

	"github.com/woo721/cursor_test/internal/config"
	"github.com/woo721/cursor_test/internal/database"
)

func samplePool() config.Pool {
	return config.Pool{
		MaxOpen:     7,
		MaxIdle:     3,
		MaxLifetime: 15 * time.Minute,
		MaxIdleTime: 2 * time.Minute,
	}
}

func writeTLSMaterial(t *testing.T) (caFile, certFile, keyFile string) {
	t.Helper()
	dir := t.TempDir()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	clientDER, err := x509.CreateCertificate(rand.Reader, clientTmpl, caTmpl, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}

	caFile = filepath.Join(dir, "ca.pem")
	certFile = filepath.Join(dir, "client.pem")
	keyFile = filepath.Join(dir, "client.key")
	writePEM(t, caFile, "CERTIFICATE", caDER)
	writePEM(t, certFile, "CERTIFICATE", clientDER)
	keyBytes, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, keyFile, "EC PRIVATE KEY", keyBytes)
	return caFile, certFile, keyFile
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: typ, Bytes: der}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenHive_UsesInjectedOpener(t *testing.T) {
	caFile, certFile, keyFile := writeTLSMaterial(t)
	var got gohive.Config
	var gotPool config.Pool
	stubDB := sql.OpenDB(stubConnector{})
	t.Cleanup(func() { _ = stubDB.Close() })

	restore := database.SetHiveOpenerForTest(func(cfg gohive.Config) *sql.DB {
		got = cfg
		return stubDB
	})
	t.Cleanup(restore)
	restorePool := database.SetConfigurePoolForTest(func(_ *sql.DB, pool config.Pool) {
		gotPool = pool
	})
	t.Cleanup(restorePool)

	cfg := config.Hive{
		Host:          "hs2.example",
		Port:          10000,
		Username:      "hiveuser",
		Password:      "hivepass",
		Database:      "analytics",
		Auth:          "NONE",
		TransportMode: "binary",
		TLS: config.TLSFiles{
			Enabled:    true,
			CAFile:     caFile,
			CertFile:   certFile,
			KeyFile:    keyFile,
			ServerName: "hs2.example",
		},
		Pool: samplePool(),
	}

	db, err := database.OpenHive(cfg)
	if err != nil {
		t.Fatalf("OpenHive: %v", err)
	}
	if db != stubDB {
		t.Fatal("expected stub db")
	}
	if got.Host != "hs2.example" || got.Port != 10000 || got.Username != "hiveuser" ||
		got.Password != "hivepass" || got.Database != "analytics" ||
		got.Auth != "NONE" || got.TransportMode != "binary" {
		t.Fatalf("unexpected gohive.Config: %+v", got)
	}
	if got.TLSConfig == nil {
		t.Fatal("expected TLSConfig")
	}
	if got.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion=%d", got.TLSConfig.MinVersion)
	}
	if got.TLSConfig.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify must be false")
	}
	if len(got.TLSConfig.Certificates) != 1 {
		t.Fatalf("expected mTLS client cert, got %d", len(got.TLSConfig.Certificates))
	}
	if got.TLSConfig.ServerName != "hs2.example" {
		t.Fatalf("ServerName=%q", got.TLSConfig.ServerName)
	}
	if gotPool != cfg.Pool {
		t.Fatalf("pool not applied: %+v", gotPool)
	}
}

func TestOpenTrino_BuildsEscapedURL(t *testing.T) {
	caFile, certFile, keyFile := writeTLSMaterial(t)
	var gotDSN string
	var registeredClient string
	var registeredTLS *tls.Config
	stubDB := sql.OpenDB(stubConnector{})
	t.Cleanup(func() { _ = stubDB.Close() })

	restore := database.SetTrinoOpenerForTest(func(dsn string) (*sql.DB, error) {
		gotDSN = dsn
		return stubDB, nil
	})
	t.Cleanup(restore)
	restoreReg := database.SetTrinoRegisterClientForTest(func(name string, client *http.Client) error {
		registeredClient = name
		if tr, ok := client.Transport.(*http.Transport); ok {
			registeredTLS = tr.TLSClientConfig
		}
		return nil
	})
	t.Cleanup(restoreReg)

	cfg := config.Trino{
		Host:         "trino.example",
		Port:         8443,
		User:         "alice@corp",
		Password:     "p@ss:word/!",
		Catalog:      "hive",
		Schema:       "analytics",
		Source:       "home-improvement-query-api",
		QueryTimeout: 12 * time.Second,
		TLS: config.TLSFiles{
			Enabled:    true,
			CAFile:     caFile,
			CertFile:   certFile,
			KeyFile:    keyFile,
			ServerName: "trino.example",
		},
		Pool: samplePool(),
	}

	db, err := database.OpenTrino(cfg)
	if err != nil {
		t.Fatalf("OpenTrino: %v", err)
	}
	if db != stubDB {
		t.Fatal("expected stub db")
	}
	u, err := url.Parse(gotDSN)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	if u.Scheme != "https" {
		t.Fatalf("scheme=%q", u.Scheme)
	}
	if u.Host != "trino.example:8443" {
		t.Fatalf("host=%q", u.Host)
	}
	if u.User.Username() != "alice@corp" {
		t.Fatalf("user=%q", u.User.Username())
	}
	pass, _ := u.User.Password()
	if pass != "p@ss:word/!" {
		t.Fatalf("password not preserved via UserPassword: %q", pass)
	}
	q := u.Query()
	if q.Get("catalog") != "hive" || q.Get("schema") != "analytics" ||
		q.Get("source") != "home-improvement-query-api" || q.Get("query_timeout") != "12s" {
		t.Fatalf("query=%v", q)
	}
	if q.Get("custom_client") == "" || registeredClient == "" || registeredClient != q.Get("custom_client") {
		t.Fatalf("expected custom TLS client registration, client=%q dsn_client=%q", registeredClient, q.Get("custom_client"))
	}
	if registeredTLS == nil || registeredTLS.MinVersion != tls.VersionTLS12 || registeredTLS.InsecureSkipVerify {
		t.Fatalf("bad registered TLS: %+v", registeredTLS)
	}
}

func TestOpenStarRocks_UsesMySQLConfig(t *testing.T) {
	caFile, _, _ := writeTLSMaterial(t)
	var got *mysql.Config
	var tlsName string

	restore := database.SetStarRocksConnectorForTest(func(cfg *mysql.Config) (driver.Connector, error) {
		cp := *cfg
		got = &cp
		return stubConnector{}, nil
	})
	t.Cleanup(restore)
	restoreTLS := database.SetMySQLRegisterTLSForTest(func(name string, tlsCfg *tls.Config) error {
		tlsName = name
		if tlsCfg.MinVersion != tls.VersionTLS12 {
			t.Fatalf("MinVersion=%d", tlsCfg.MinVersion)
		}
		if tlsCfg.InsecureSkipVerify {
			t.Fatal("InsecureSkipVerify must be false")
		}
		return nil
	})
	t.Cleanup(restoreTLS)

	cfg := config.StarRocks{
		Host:         "sr.example",
		Port:         9030,
		Username:     "root",
		Password:     "sr-secret",
		Database:     "analytics",
		DialTimeout:  5 * time.Second,
		ReadTimeout:  8 * time.Second,
		WriteTimeout: 9 * time.Second,
		TLS: config.TLSFiles{
			Enabled:    true,
			CAFile:     caFile,
			ServerName: "sr.example",
		},
		Pool: samplePool(),
	}

	db, err := database.OpenStarRocks(cfg)
	if err != nil {
		t.Fatalf("OpenStarRocks: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if got == nil {
		t.Fatal("mysql.Config not captured")
	}
	if got.User != "root" || got.Passwd != "sr-secret" || got.Net != "tcp" ||
		got.Addr != "sr.example:9030" || got.DBName != "analytics" {
		t.Fatalf("unexpected mysql.Config identity: %+v", got)
	}
	if !got.ParseTime {
		t.Fatal("ParseTime should be true")
	}
	if got.Timeout != 5*time.Second || got.ReadTimeout != 8*time.Second || got.WriteTimeout != 9*time.Second {
		t.Fatalf("timeouts: dial=%v read=%v write=%v", got.Timeout, got.ReadTimeout, got.WriteTimeout)
	}
	if tlsName == "" || got.TLSConfig != tlsName {
		t.Fatalf("TLS registration mismatch name=%q cfg=%q", tlsName, got.TLSConfig)
	}
}

func TestConfigurePool(t *testing.T) {
	db := sql.OpenDB(stubConnector{})
	t.Cleanup(func() { _ = db.Close() })
	pool := samplePool()
	database.ConfigurePool(db, pool)
	stats := db.Stats()
	if stats.MaxOpenConnections != pool.MaxOpen {
		t.Fatalf("MaxOpenConnections=%d", stats.MaxOpenConnections)
	}
}

type stubConnector struct{}

func (stubConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, driver.ErrSkip
}

func (stubConnector) Driver() driver.Driver { return stubDriver{} }

type stubDriver struct{}

func (stubDriver) Open(string) (driver.Conn, error) { return nil, driver.ErrSkip }
