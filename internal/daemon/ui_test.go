package daemon

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/api"
	"github.com/otfabric/lan-tracker/internal/clock"
	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/platform/fake"
)

// TestUIEnabled starts the daemon with ui.enabled: the token is created
// beside the database, the socket hands out the URL, and the listener
// serves the API to that token only.
func TestUIEnabled(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listen := l.Addr().String()
	_ = l.Close()
	h := start(t, `version: 1
interfaces:
  - name: eth1
storage: { path: $DIR/data/hosts.db }
api: { socket: $DIR/api.sock }
ui: { enabled: true, listen: "`+listen+`" }
logging: { format: text }
`, 0)
	defer h.stop()

	tokenPath := filepath.Join(h.dir, "data", UITokenFile)
	fi, err := os.Stat(tokenPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", err, fi)
	}
	u, err := api.NewClient(filepath.Join(h.dir, "api.sock"), 5*time.Second).UI(context.Background())
	if err != nil || !u.Enabled || u.Listen != listen {
		t.Fatalf("ui info: %v %+v", err, u)
	}
	_, token, ok := strings.Cut(u.URL, "#token=")
	if b, _ := os.ReadFile(tokenPath); !ok || strings.TrimSpace(string(b)) != token {
		t.Fatalf("url %q does not carry the token file's token", u.URL)
	}

	req, _ := http.NewRequest(http.MethodGet, "http://"+listen+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status through the UI listener: %d", resp.StatusCode)
	}
	if resp, err = http.Get("http://" + listen + "/v1/status"); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status without token: %d", resp.StatusCode)
	}
}

// A token file that is not a token stops the start, with the path.
func TestUIBadToken(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, UITokenFile), []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := "version: 1\ninterfaces:\n  - name: eth1\nstorage: { path: " + filepath.Join(dir, "hosts.db") + " }\n" +
		"api: { socket: " + filepath.Join(dir, "api.sock") + " }\nui: { enabled: true, listen: \"127.0.0.1:1\" }\nlogging: { format: text }\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	backends, _, _, _, _ := fake.Backends()
	d, err := New(Options{Load: config.LoadOptions{Path: cfgPath}, Stderr: &syncBuffer{}, Clock: clock.NewSim(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)),
		Backends: &backends, Notifier: &fakeNotifier{}, Signals: make(chan os.Signal, 1)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = d.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "not a token") {
		t.Fatalf("start with a bad token: %v", err)
	}
}

// TestOptionalListenerBusy: when another process holds the port of
// api.listen or ui.listen, the daemon still starts on its socket, reports
// the listener in the status (degraded) and binds it once the port is
// free.
func TestOptionalListenerBusy(t *testing.T) {
	for _, key := range []string{"api.listen", "ui.listen"} {
		t.Run(key, func(t *testing.T) {
			busy, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := busy.Addr().String()
			cfg := "version: 1\ninterfaces:\n  - name: eth1\nstorage: { path: $DIR/data/hosts.db }\nlogging: { format: text }\n"
			if key == "api.listen" {
				cfg += `api: { socket: $DIR/api.sock, listen: "` + addr + `" }` + "\n"
			} else {
				cfg += "api: { socket: $DIR/api.sock }\n" + `ui: { enabled: true, listen: "` + addr + `" }` + "\n"
			}
			h := start(t, cfg, 0)
			defer h.stop()

			c := api.NewClient(filepath.Join(h.dir, "api.sock"), 5*time.Second)
			st, err := c.Status(context.Background())
			if err != nil || st.State != api.StateDegraded || len(st.Problems) != 1 || !strings.Contains(st.Problems[0], key+" "+addr) {
				t.Fatalf("status with %s busy: %v %s %q", key, err, st.State, st.Problems)
			}
			h.waitLog(key + " not listening")

			_ = busy.Close()
			deadline := time.Now().Add(10 * time.Second)
			for {
				st, err = c.Status(context.Background())
				if err == nil && len(st.Problems) == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("%s never bound: %v %q", key, err, st.Problems)
				}
				time.Sleep(50 * time.Millisecond)
			}
			if st.State != api.StateOK {
				t.Errorf("state after %s bound: %s", key, st.State)
			}
			resp, err := http.Get("http://" + addr + "/v1/status")
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			// api.listen answers reads; ui.listen asks for its token.
			if want := map[string]int{"api.listen": http.StatusOK, "ui.listen": http.StatusUnauthorized}[key]; resp.StatusCode != want {
				t.Errorf("%s after binding: %d, want %d", key, resp.StatusCode, want)
			}
		})
	}
}
