package bootstrap

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func TestDatabaseURLControlsStartup(t *testing.T) {
	freeAddress := func(t *testing.T) string {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		_ = listener.Close()
		return address
	}

	t.Run("unreachable database fails startup before serving", func(t *testing.T) {
		t.Setenv("HTTP_ADDR", freeAddress(t))
		t.Setenv("LOG_LEVEL", "error")
		t.Setenv("DATABASE_URL", "postgres://nobody@127.0.0.1:1/jungle?sslmode=disable&connect_timeout=1")
		app := New()
		if err := app.Err(); err != nil {
			t.Fatalf("dependency graph is invalid: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Start(ctx); err == nil {
			_ = app.Stop(ctx)
			t.Fatal("application started despite an unreachable database")
		}
	})

	t.Run("invalid database url fails application construction", func(t *testing.T) {
		t.Setenv("HTTP_ADDR", freeAddress(t))
		t.Setenv("LOG_LEVEL", "error")
		t.Setenv("DATABASE_URL", "%zz://not a url")
		// fx.New runs the invokes that build the pool, so an unparseable URL
		// must fail before any start hook runs.
		app := New()
		if err := app.Err(); err == nil {
			_ = app.Stop(context.Background())
			t.Fatal("application was constructed despite an invalid DATABASE_URL")
		}
	})

	t.Run("without database url the application starts http only", func(t *testing.T) {
		t.Setenv("HTTP_ADDR", freeAddress(t))
		t.Setenv("LOG_LEVEL", "error")
		t.Setenv("DATABASE_URL", "")
		if err := os.Unsetenv("DATABASE_URL"); err != nil {
			t.Fatal(err)
		}
		app := New()
		if err := app.Err(); err != nil {
			t.Fatalf("dependency graph is invalid: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := app.Start(ctx); err != nil {
			t.Fatalf("application must start without a database: %v", err)
		}
		if err := app.Stop(ctx); err != nil {
			t.Fatalf("stop: %v", err)
		}
	})
}
