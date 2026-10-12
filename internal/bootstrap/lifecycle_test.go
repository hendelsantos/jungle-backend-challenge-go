package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	httpapi "github.com/hendelsantos/jungle-backend-go/internal/adapters/http"
	"go.uber.org/fx"
)

func TestHTTPStartsAndReleasesListener(t *testing.T) {
	server := httpapi.NewServer("127.0.0.1:0")
	addresses := make(chan string, 1)
	server.BaseContext = func(listener net.Listener) context.Context {
		addresses <- listener.Addr().String()
		return context.Background()
	}
	app := testApp(server)
	startCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(startCtx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	var address string
	select {
	case address = <-addresses:
	case <-startCtx.Done():
		t.Fatal(startCtx.Err())
	}
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Get("http://" + address + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "{\"status\":\"ok\"}\n" {
		t.Fatalf("health response: %d, %s, %v", response.StatusCode, body, err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("listener was not released: %v", err)
	}
	_ = listener.Close()
}

func TestProductionAppRejectsOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("HTTP_ADDR", listener.Addr().String())
	t.Setenv("LOG_LEVEL", "error")
	app := New()
	if err := app.Err(); err != nil {
		t.Fatalf("dependency graph is invalid: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := app.Start(ctx); err == nil {
		_ = app.Stop(ctx)
		t.Fatal("application started despite an occupied port")
	}
}

func TestProductionAppRejectsInvalidConfig(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":invalid")
	t.Setenv("LOG_LEVEL", "info")
	if err := New().Err(); err == nil {
		t.Fatal("application accepted invalid configuration")
	}
}

func TestShutdownWaitsForActiveRequest(t *testing.T) {
	server := httpapi.NewServer("127.0.0.1:0")
	addresses := make(chan string, 1)
	server.BaseContext = func(listener net.Listener) context.Context {
		addresses <- listener.Addr().String()
		return context.Background()
	}
	entered, release := make(chan struct{}), make(chan struct{})
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_, _ = w.Write([]byte("completed"))
	})
	defer close(release)
	app := testApp(server)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	var address string
	select {
	case address = <-addresses:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	requestDone := make(chan error, 1)
	go func() {
		response, err := client.Get("http://" + address)
		if err == nil {
			_, err = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	stopping := make(chan struct{})
	server.RegisterOnShutdown(func() { close(stopping) })
	stopped := make(chan error, 1)
	go func() { stopped <- app.Stop(ctx) }()
	select {
	case <-stopping:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-stopped:
		t.Fatalf("shutdown did not wait for the active request: %v", err)
	default:
	}
	release <- struct{}{}
	if err := <-requestDone; err != nil {
		t.Fatalf("active request failed: %v", err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
}

func testApp(server *http.Server) *fx.App {
	return fx.New(
		fx.NopLogger,
		fx.Supply(server, slog.New(slog.NewTextHandler(io.Discard, nil))),
		fx.Invoke(registerHTTP),
	)
}
