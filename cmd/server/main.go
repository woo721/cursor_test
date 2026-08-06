package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/woo721/cursor_test/internal/bootstrap"
	"github.com/woo721/cursor_test/internal/config"
)

var (
	buildApplication = bootstrap.Build
	listen           = net.Listen
	shutdownServer   = func(s *http.Server, ctx context.Context) error { return s.Shutdown(ctx) }
	closeApplication = func(app *bootstrap.Application) error { return app.Close() }
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string, stdout io.Writer) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	application, err := buildApplication(cfg)
	if err != nil {
		return err
	}

	ln, err := listen("tcp", cfg.Server.Addr)
	if err != nil {
		return errors.Join(err, closeApplication(application))
	}
	if stdout != nil {
		fmt.Fprintf(stdout, "listening %s\n", ln.Addr().String())
	}

	server := &http.Server{Handler: application.Handler}
	serveErr := make(chan error, 1)
	go func() {
		err := server.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case <-ctx.Done():
		// 进程生命周期由外层信号 context 驱动，关闭窗口来自配置而非请求 context。
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
		defer cancel()
		err := shutdownServer(server, shutdownCtx)
		return errors.Join(err, <-serveErr, closeApplication(application))
	case err := <-serveErr:
		return errors.Join(err, closeApplication(application))
	}
}
