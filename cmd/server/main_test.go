package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/woo721/cursor_test/internal/bootstrap"
	"github.com/woo721/cursor_test/internal/config"
)

func env(overrides map[string]string) func(string) string {
	values := map[string]string{
		"API_KEY":          "test-api-key",
		"APP_ADDR":         "127.0.0.1:0",
		"SHUTDOWN_TIMEOUT": "123ms",
	}
	for k, v := range overrides {
		values[k] = v
	}
	return func(key string) string {
		return values[key]
	}
}

func TestRunStartsRespondsAndShutsDownGracefully(t *testing.T) {
	restore := replaceHooks(t)
	defer restore()

	var mu sync.Mutex
	var events []string
	appendEvent := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}

	buildApplication = func(config.Config) (*bootstrap.Application, error) {
		return &bootstrap.Application{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("ok"))
			}),
		}, nil
	}

	addrCh := make(chan string, 1)
	listen = func(network, address string) (net.Listener, error) {
		ln, err := net.Listen(network, address)
		if err == nil {
			addrCh <- ln.Addr().String()
		}
		return ln, err
	}

	var shutdownCalled bool
	var timeoutSeen bool
	shutdownServer = func(s *http.Server, ctx context.Context) error {
		appendEvent("shutdown")
		shutdownCalled = true
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			timeoutSeen = remaining > 0 && remaining <= 123*time.Millisecond
		}
		return s.Shutdown(ctx)
	}
	closeApplication = func(app *bootstrap.Application) error {
		appendEvent("close")
		return app.Close()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, env(nil), io.Discard)
	}()

	var addr string
	select {
	case addr = <-addrCh:
	case <-time.After(time.Second):
		t.Fatal("server did not start")
	}
	resp, err := http.Get("http://" + addr)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run did not exit after cancellation")
	}
	if !shutdownCalled {
		t.Fatal("context cancellation did not call Shutdown")
	}
	if !timeoutSeen {
		t.Fatal("Shutdown did not use configured timeout")
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(events, ","); got != "shutdown,close" {
		t.Fatalf("events=%s want shutdown,close", got)
	}
}

func TestRunReturnsStartupErrors(t *testing.T) {
	restore := replaceHooks(t)
	defer restore()

	if err := run(context.Background(), func(string) string { return "" }, io.Discard); err == nil {
		t.Fatal("expected config error")
	}

	want := errors.New("build failed")
	buildApplication = func(config.Config) (*bootstrap.Application, error) {
		return nil, want
	}
	err := run(context.Background(), env(nil), io.Discard)
	if !errors.Is(err, want) {
		t.Fatalf("run error=%v want %v", err, want)
	}
}

func TestRunWritesListeningAddress(t *testing.T) {
	restore := replaceHooks(t)
	defer restore()

	buildApplication = func(config.Config) (*bootstrap.Application, error) {
		return &bootstrap.Application{Handler: http.NotFoundHandler()}, nil
	}
	addrCh := make(chan struct{}, 1)
	listen = func(network, address string) (net.Listener, error) {
		ln, err := net.Listen(network, address)
		if err != nil {
			return nil, err
		}
		addrCh <- struct{}{}
		return ln, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-addrCh
		cancel()
	}()
	var out bytes.Buffer
	err := run(ctx, env(nil), &out)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "listening ") {
		t.Fatalf("stdout=%q", out.String())
	}
}

func replaceHooks(t *testing.T) func() {
	t.Helper()
	prevBuild := buildApplication
	prevListen := listen
	prevShutdown := shutdownServer
	prevClose := closeApplication
	return func() {
		buildApplication = prevBuild
		listen = prevListen
		shutdownServer = prevShutdown
		closeApplication = prevClose
	}
}
