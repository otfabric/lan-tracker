package daemon

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/api"
	"github.com/otfabric/lan-tracker/internal/clock"
	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/platform/fake"
	"github.com/otfabric/lan-tracker/internal/service"
	"github.com/otfabric/lan-tracker/internal/store"
)

// TestStartupFailures: a start that cannot complete stops with an error
// naming the cause, before the daemon reports ready, and leaves no API
// socket behind.
func TestStartupFailures(t *testing.T) {
	tests := []struct {
		name  string
		extra string // appended to the configuration
		// prepare damages the directory or database before the start.
		prepare func(t *testing.T, dir string)
		newErr  string // New fails with this
		runErr  string // Run fails with this
	}{
		{name: "database directory is a file", prepare: func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "data"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}, runErr: "open store"},
		{name: "OUI override missing", extra: "identity: { oui_override: $DIR/missing.tsv }\n", runErr: "missing.tsv"},
		{name: "kill switch unreadable", prepare: damage(`ALTER TABLE runtime_state RENAME TO damaged`), runErr: "read the kill switch"},
		{name: "correlator state unreadable", prepare: damage(`ALTER TABLE hosts RENAME TO damaged`), runErr: "start correlator"},
		{name: "replay file missing", newErr: "missing.pcapng"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := testConfig(dir, "info", strings.ReplaceAll(tt.extra, "$DIR", dir))
			if tt.newErr != "" {
				cfg = "version: 1\ninterfaces:\n  - name: eth1\n    prefixes: [192.168.110.0/24]\n    replay: { file: " + filepath.Join(dir, "missing.pcapng") +
					" }\nstorage: { path: " + filepath.Join(dir, "hosts.db") + " }\nlogging: { format: text }\n"
			}
			cfgPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			if tt.prepare != nil {
				tt.prepare(t, dir)
			}
			log := &syncBuffer{}
			backends, _, _, _, _ := fake.Backends()
			notifier := &fakeNotifier{}
			d, err := New(Options{Load: config.LoadOptions{Path: cfgPath}, Stderr: log, Clock: clock.NewSim(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)),
				Backends: &backends, Notifier: notifier, Signals: make(chan os.Signal, 1)})
			if tt.newErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.newErr) {
					t.Fatalf("New: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := d.Run(ctx); err == nil || !strings.Contains(err.Error(), tt.runErr) {
				t.Fatalf("Run: %v\n%s", err, log)
			}
			if n := notifier.count("READY"); n != 0 {
				t.Errorf("READY sent %d times by a failed start", n)
			}
			if _, err := os.Stat(filepath.Join(dir, "api.sock")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("api socket after a failed start: %v", err)
			}
		})
	}
}

// damage creates the database and applies stmt to it, as another writer
// could have left it.
func damage(stmt string) func(t *testing.T, dir string) {
	return func(t *testing.T, dir string) {
		t.Helper()
		path := filepath.Join(dir, "data", "hosts.db")
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(context.Background(), store.Options{Path: path, Clock: clock.NewSim(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
}

// failingNotifier is a service manager that cannot be reached.
type failingNotifier struct{ fakeNotifier }

func (f *failingNotifier) Ready() error {
	_ = f.record("READY")
	return errors.New("notify socket gone")
}

func (f *failingNotifier) Stopping() error {
	_ = f.record("STOPPING")
	return errors.New("notify socket gone")
}

// A service manager that cannot be told is logged, not fatal; without a
// signal channel the daemon installs its own and stops when ctx ends.
func TestNotificationFailureAndOwnSignals(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(testConfig(dir, "info", "")), 0o600); err != nil {
		t.Fatal(err)
	}
	log := &syncBuffer{}
	backends, _, _, _, _ := fake.Backends()
	d, err := New(Options{Load: config.LoadOptions{Path: cfgPath}, Stderr: log, Clock: clock.NewSim(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)),
		Backends: &backends, Notifier: &failingNotifier{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- d.Run(ctx) }()
	select {
	case <-d.Ready():
	case err := <-errc:
		t.Fatalf("Run: %v\n%s", err, log)
	case <-time.After(10 * time.Second):
		t.Fatalf("not ready\n%s", log)
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("did not stop")
	}
	for _, want := range []string{"notification=ready", "notification=stopping", "notify socket gone", "context cancelled"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

// TestLogFormatFallback: a compiled-default format on a terminal logs text,
// and journald without its socket falls back to text with a warning.
func TestLogFormatFallback(t *testing.T) {
	for _, tt := range []struct {
		name     string
		format   string
		terminal bool
		want     string
	}{
		{"default on a terminal", "", true, "level=INFO"},
		{"journald without its socket", "journald", false, "logging to stderr instead of journald"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.format == "journald" && service.JournalAvailable() {
				t.Skip("journald is reachable here")
			}
			dir := t.TempDir()
			cfg := "version: 1\ninterfaces:\n  - name: eth1\nstorage: { path: " + filepath.Join(dir, "hosts.db") + " }\n"
			if tt.format != "" {
				cfg += "logging: { format: " + tt.format + " }\n"
			}
			cfgPath := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			log := &syncBuffer{}
			d, err := New(Options{Load: config.LoadOptions{Path: cfgPath}, Stderr: log, StderrIsTerminal: tt.terminal})
			if err != nil {
				t.Fatal(err)
			}
			d.log.Info("probe line")
			if !strings.Contains(log.String(), tt.want) || !strings.Contains(log.String(), "msg=\"probe line\"") {
				t.Errorf("log:\n%s", log)
			}
		})
	}
}

// TestReloadKeepsRestartOnlyKeys: a reload never applies a key that only a
// restart can change (ARCHITECTURE.md §6); it reports it as not applied
// and keeps the running value.
func TestReloadKeepsRestartOnlyKeys(t *testing.T) {
	for _, tt := range []struct {
		key    string
		edit   func(cfg string) string
		kept   func(*config.Config) bool
		listed string // the key reported as not applied
	}{
		{"logging.format", func(cfg string) string { return strings.Replace(cfg, "format: text", "format: json", 1) },
			func(c *config.Config) bool { return c.Logging.Format == "text" }, "logging.format"},
		{"replay", func(cfg string) string { return cfg + "replay: { exit_when_done: true }\n" },
			func(c *config.Config) bool { return !c.Replay.ExitWhenDone }, "replay."},
		{"tcp per interface", func(cfg string) string {
			return cfg + "active: { budgets: { tcp: { max_concurrent_per_interface: 3 } } }\n"
		}, func(c *config.Config) bool { return c.Active.Budgets.TCP.MaxConcurrentPerInterface == 2 }, "active.budgets.tcp.max_concurrent_per_interface"},
		{"tcp per host", func(cfg string) string {
			return cfg + "active: { budgets: { tcp: { max_concurrent_per_host: 2 } } }\n"
		}, func(c *config.Config) bool { return c.Active.Budgets.TCP.MaxConcurrentPerHost == 1 }, "active.budgets.tcp.max_concurrent_per_host"},
	} {
		t.Run(tt.key, func(t *testing.T) {
			h := start(t, testConfig("$DIR", "info", ""), 0)
			defer h.stop()
			h.writeConfig(tt.edit(testConfig(h.dir, "info", "")))
			res, err := h.d.ReloadConfig(context.Background(), "bart")
			if err != nil {
				t.Fatal(err)
			}
			listed := false
			for _, c := range res.NotApplied {
				listed = listed || strings.HasPrefix(c.Key, tt.listed)
			}
			if !listed || len(res.Applied) != 0 || !tt.kept(h.d.Config()) {
				t.Errorf("applied %+v, not applied %+v, running %+v", res.Applied, res.NotApplied, h.d.Config())
			}
			h.waitLog("need a restart")
		})
	}
}

// daemon status reports each interface's link (down, absent) and turns
// unhealthy, with the database's error as a problem, when the database
// cannot answer.
func TestStatusOfLinksAndDamage(t *testing.T) {
	cfg := "version: 1\ninterfaces:\n  - name: eth1\n  - name: eth2\nstorage: { path: $DIR/hosts.db }\napi: { socket: $DIR/api.sock }\nlogging: { format: text }\n"
	h := startWith(t, cfg, 0, func(_ *fake.Neighbors, i *fake.Interfaces) {
		i.SetLinks([]platform.Link{{Name: "eth1", Index: 2, MAC: net.HardwareAddr{2, 0, 0, 0, 0, 9}}})
	})
	defer h.stop()
	client := api.NewClient(filepath.Join(h.dir, "api.sock"), 5*time.Second)
	ctx := context.Background()
	states := func() map[string]string {
		ifs, err := client.Interfaces(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, i := range ifs {
			out[i.Name] = i.State
		}
		return out
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if s := states(); s["eth1"] == "down" && s["eth2"] == "absent" {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("interface states = %v", s)
		}
	}

	db, err := sql.Open("sqlite", "file:"+filepath.Join(h.dir, "hosts.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `ALTER TABLE network_contexts RENAME TO damaged`); err != nil {
		t.Fatal(err)
	}
	st, err := client.Status(ctx)
	if err != nil || st.State != api.StateUnhealthy || st.Database.OK || !strings.Contains(st.Database.Error, "no such table") ||
		len(st.Problems) == 0 || !strings.Contains(st.Problems[len(st.Problems)-1], "no such table") {
		t.Errorf("status of a damaged database: %v %+v", err, st)
	}
	if _, err := client.Interfaces(ctx); err == nil || !strings.Contains(err.Error(), "no such table") {
		t.Errorf("interfaces of a damaged database: %v", err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE damaged RENAME TO network_contexts`); err != nil {
		t.Fatal(err)
	}
}
