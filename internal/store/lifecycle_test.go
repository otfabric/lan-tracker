package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Open refuses a database it cannot place, and every request after Close
// fails with ErrClosed instead of blocking on a writer that has gone.
func TestOpenAndClosedStore(t *testing.T) {
	if _, err := Open(context.Background(), Options{}); err == nil || !strings.Contains(err.Error(), "empty database path") {
		t.Errorf("empty path: %v", err)
	}
	file := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), Options{Path: filepath.Join(file, "hosts.db")}); err == nil ||
		!strings.Contains(err.Error(), "create database directory") {
		t.Errorf("directory is a file: %v", err)
	}

	s := open(t, Options{})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"flush": func() error { return s.Flush(ctx) },
		"ping":  func() error { return s.Ping(ctx) },
		"view":  func() error { return s.View(ctx, func(context.Context, *sql.Tx) error { return nil }) },
		"close": s.Close,
	} {
		err := call()
		if name == "close" && err != nil || name != "close" && !errors.Is(err, ErrClosed) {
			t.Errorf("%s after Close: %v", name, err)
		}
	}
}
