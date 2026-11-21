package api_test

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/otfabric/lan-tracker/internal/api"
	"github.com/otfabric/lan-tracker/internal/store"
)

func TestLoadUIToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui.token")
	tok, err := api.LoadUIToken(path, nil)
	if err != nil || len(tok) < 40 {
		t.Fatalf("new token: %v %q", err, tok)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file: %v %v", err, fi)
	}
	// The same token after a restart.
	again, err := api.LoadUIToken(path, nil)
	if err != nil || again != tok {
		t.Errorf("reload: %v %q != %q", err, again, tok)
	}
	if other, _ := api.LoadUIToken(filepath.Join(t.TempDir(), "ui.token"), nil); other == tok {
		t.Error("two new tokens are equal")
	}

	for name, content := range map[string]string{"short": "abc\n", "spaces": strings.Repeat("a", 20) + " " + strings.Repeat("b", 20)} {
		p := filepath.Join(t.TempDir(), "ui.token")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := api.LoadUIToken(p, nil); err == nil || !strings.Contains(err.Error(), "not a token") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := api.LoadUIToken(filepath.Join(t.TempDir(), "missing", "ui.token"), nil); err == nil {
		t.Error("token in a missing directory: no error")
	}
	if _, err := api.LoadUIToken(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("token path is a directory: %v", err)
	}
}

// TestUITokenWideModeReplaced: a token file other users could read is
// replaced with a new token (mode 0600), and the replacement is logged.
func TestUITokenWideModeReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui.token")
	old := strings.Repeat("o", 43)
	if err := os.WriteFile(path, []byte(old+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	tok, err := api.LoadUIToken(path, slog.New(slog.NewTextHandler(&log, nil)))
	if err != nil || tok == old || len(tok) < 40 {
		t.Fatalf("wide token file: %v %q", err, tok)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("replaced file: %v %v", err, fi)
	}
	if b, _ := os.ReadFile(path); strings.TrimSpace(string(b)) != tok {
		t.Errorf("file holds %q, want the new token", b)
	}
	if !strings.Contains(log.String(), "level=INFO") || !strings.Contains(log.String(), "replaced it with a new token") {
		t.Errorf("log: %s", log.String())
	}

	// A file another user owns is replaced too (needs root to chown).
	if os.Geteuid() != 0 {
		return
	}
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	again, err := api.LoadUIToken(path, nil)
	if err != nil || again == tok {
		t.Errorf("token file of another user: %v %q", err, again)
	}
}

// TestUITokenUnusable: a token file the daemon cannot open, or a wide one
// it cannot replace, stops the start with the path (as a user without
// root's file access).
// TestUITokenWriteFails: a new token whose write fails leaves no partial
// file behind, so the next start creates a new one.
func TestUITokenWriteFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ui.token")
	restore := api.FailUITokenWrite()
	_, err := api.LoadUIToken(path, nil)
	restore()
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("failed write: %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("partial token file left: %v", err)
	}
	if tok, err := api.LoadUIToken(path, nil); err != nil || len(tok) < 40 {
		t.Errorf("next start: %v %q", err, tok)
	}
}

func TestUITokenUnusable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	closed := filepath.Join(t.TempDir(), "ui.token")
	if err := os.WriteFile(closed, []byte(strings.Repeat("c", 43)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(closed, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := api.LoadUIToken(closed, nil); err == nil || !strings.Contains(err.Error(), closed) {
		t.Errorf("unreadable token file: %v", err)
	}
	dir := t.TempDir()
	wide := filepath.Join(dir, "ui.token")
	if err := os.WriteFile(wide, []byte(strings.Repeat("w", 43)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wide, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := api.LoadUIToken(wide, nil); err == nil || !strings.Contains(err.Error(), wide) {
		t.Errorf("wide token file in a read-only directory: %v", err)
	}
}

// TestUITokenSymlinkRefused: a symbolic link at the token path is refused,
// whether its target exists or not, and its target is left alone.
func TestUITokenSymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte(strings.Repeat("x", 43)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, dest := range map[string]string{"existing": target, "dangling": filepath.Join(dir, "missing")} {
		link := filepath.Join(t.TempDir(), "ui.token")
		if err := os.Symlink(dest, link); err != nil {
			t.Fatal(err)
		}
		if _, err := api.LoadUIToken(link, nil); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Errorf("dangling link target created: %v", err)
	}
}

type uiFixture struct {
	base, token, socket string
	ctl                 *control
}

func startUI(t *testing.T, assets fs.FS) *uiFixture {
	t.Helper()
	f := &uiFixture{token: strings.Repeat("t", 43), ctl: &control{}, socket: filepath.Join(t.TempDir(), "api.sock")}
	listen := freePort(t)
	srv := api.New(api.Options{
		Control: f.ctl,
		Status:  func(context.Context) api.Status { return api.Status{Version: "test", State: api.StateOK} },
		UI:      &api.UIOptions{Listen: listen, Token: f.token, Assets: assets},
	})
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx, f.socket, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = srv.Wait() })
	f.base = "http://" + listen
	return f
}

// uiAnswer is what a request to the fixture got back; the body is read and
// closed by do.
type uiAnswer struct {
	status int
	header http.Header
	body   string
}

func (f *uiFixture) do(t *testing.T, method, path, token, host, body string) uiAnswer {
	t.Helper()
	req, err := http.NewRequest(method, f.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return uiAnswer{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

func TestUIListener(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":     {Data: []byte("<!doctype html><title>LAN Tracker</title>")},
		"assets/app.js":  {Data: []byte("console.log(1)")},
		"favicon.svg":    {Data: []byte("<svg/>")},
		".gitkeep":       {Data: nil},
		"assets/sub/x.a": {Data: []byte("x")},
	}
	f := startUI(t, assets)
	tok := f.token

	for _, c := range []struct {
		name, method, path, token, host, body string
		status                                int
		contains                              string
	}{
		{"index", "GET", "/", "", "", "", 200, "LAN Tracker"},
		{"view reload", "GET", "/hosts/abc", "", "", "", 200, "LAN Tracker"},
		{"index by name", "GET", "/favicon.svg", "", "", "", 200, "<svg/>"},
		{"hidden file", "GET", "/.gitkeep", "", "", "", 200, "LAN Tracker"},
		{"asset", "GET", "/assets/app.js", "", "", "", 200, "console.log"},
		{"missing asset", "GET", "/assets/nope.js", "", "", "", 404, ""},
		{"post page", "POST", "/", "", "", "", 405, ""},
		{"not loopback", "GET", "/", "", "lan-tracker.example:9735", "", 403, "loopback"},
		{"api not loopback", "GET", "/v1/status", tok, "evil.example", "", 403, "loopback"},
		{"no token", "GET", "/v1/status", "", "", "", 401, "token"},
		{"wrong token", "GET", "/v1/status", "x" + tok[1:], "", "", 401, "token"},
		{"status", "GET", "/v1/status", tok, "", "", 200, `"version":"test"`},
		{"localhost", "GET", "/v1/status", tok, "localhost:1", "", 200, `"version":"test"`},
		{"token info not here", "GET", "/v1/ui", tok, "", "", 404, "no such endpoint"},
		{"kill switch", "POST", "/v1/active/disable", tok, "", `{"reason":"x"}`, 405, "host descriptions"},
		{"reload", "POST", "/v1/config/reload", tok, "", `{}`, 405, "host descriptions"},
		{"bad description path", "POST", "/v1/hosts/a/b/description", tok, "", `{"description":"x"}`, 405, ""},
		{"delete", "DELETE", "/v1/hosts/h1/description", tok, "", "", 405, ""},
		{"description", "POST", "/v1/hosts/h1/description", tok, "", `{"description":"Inverter, roof"}`, 200, "Inverter, roof"},
	} {
		a := f.do(t, c.method, c.path, c.token, c.host, c.body)
		if a.status != c.status || !strings.Contains(a.body, c.contains) {
			t.Errorf("%s: %d %q", c.name, a.status, a.body)
		}
		if csp := a.header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: CSP %q", c.name, csp)
		}
		if strings.HasPrefix(c.path, "/v1/") && a.header.Get("Cache-Control") != "no-store" && c.status != 403 {
			t.Errorf("%s: API answer cacheable: %q", c.name, a.header.Get("Cache-Control"))
		}
	}
	if a := f.do(t, "GET", "/assets/app.js", "", "", ""); !strings.Contains(a.header.Get("Cache-Control"), "immutable") {
		t.Errorf("asset cache: %q", a.header.Get("Cache-Control"))
	}
	if a := f.do(t, "GET", "/", "", "", ""); a.header.Get("Cache-Control") != "no-cache" {
		t.Errorf("index cache: %q", a.header.Get("Cache-Control"))
	}
	// The description was set by the actor "ui", never by a Unix user.
	f.ctl.mu.Lock()
	defer f.ctl.mu.Unlock()
	if len(f.ctl.actors) != 1 || f.ctl.actors[0] != api.UIActor || f.ctl.reasons[0] != "h1=Inverter, roof" {
		t.Errorf("description changes: %v %v", f.ctl.actors, f.ctl.reasons)
	}

	// The socket hands out the URL with the token.
	u, err := api.NewClient(f.socket, 5*time.Second).UI(context.Background())
	if err != nil || !u.Enabled || u.URL != f.base+"/#token="+tok || "http://"+u.Listen != f.base {
		t.Errorf("ui info: %v %+v", err, u)
	}
}

func TestUIWithoutBuild(t *testing.T) {
	for name, assets := range map[string]fs.FS{"nil": nil, "placeholder": fstest.MapFS{".gitkeep": {}}} {
		f := startUI(t, assets)
		a := f.do(t, "GET", "/", "", "", "")
		if a.status != 200 || !strings.Contains(a.body, "built without its web UI") {
			t.Errorf("%s: %d %q", name, a.status, a.body)
		}
		if a := f.do(t, "PUT", "/", "", "", ""); a.status != 405 {
			t.Errorf("%s: PUT %d", name, a.status)
		}
	}
}

func TestUIDisabled(t *testing.T) {
	f := start(t)
	u, err := f.client.UI(context.Background())
	if err != nil || u.Enabled || u.URL != "" {
		t.Errorf("ui info: %v %+v", err, u)
	}
	// The read-only TCP listener does not hand out the token either.
	resp, err := http.Get("http://" + f.listen + "/v1/ui")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("api.listen /v1/ui: %d", resp.StatusCode)
	}
}

// TestUIListenerInUse: a busy ui.listen port does not stop the start; the
// server serves the socket, reports the listener and binds it once the
// port is free.
func TestUIListenerInUse(t *testing.T) {
	defer api.SetListenRetry(10*time.Millisecond, 40*time.Millisecond)()
	busy := freePort(t)
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", busy)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "api.sock")
	srv := api.New(api.Options{UI: &api.UIOptions{Listen: busy, Token: strings.Repeat("t", 43)}})
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx, socket, ""); err != nil {
		t.Fatalf("busy ui port: %v", err)
	}
	waitUntil(t, func() bool { p := srv.Problems(); return len(p) == 1 && strings.Contains(p[0], "ui.listen "+busy) })
	if _, err := os.Stat(socket); err != nil {
		t.Errorf("socket: %v", err)
	}
	_ = l.Close()
	waitUntil(t, func() bool { return len(srv.Problems()) == 0 })
	resp, err := http.Get("http://" + busy + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("ui after binding: %d", resp.StatusCode)
	}
	cancel()
	if err := srv.Wait(); err != nil {
		t.Errorf("wait: %v", err)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Errorf("socket left after shutdown: %v", err)
	}
}

// TestOptionalListenerStopsRetrying: a listener that never binds is given
// up when the server stops, and Wait returns.
func TestOptionalListenerStopsRetrying(t *testing.T) {
	defer api.SetListenRetry(10*time.Millisecond, 40*time.Millisecond)()
	busy := freePort(t)
	l, err := net.Listen("tcp", busy)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	srv := api.New(api.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx, filepath.Join(t.TempDir(), "api.sock"), busy); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return len(srv.Problems()) == 1 })
	time.Sleep(150 * time.Millisecond) // several retries fail too
	if p := srv.Problems(); len(p) != 1 {
		t.Errorf("problems while retrying: %q", p)
	}
	cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("wait: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop while retrying a listener")
	}
	if p := srv.Problems(); len(p) != 0 {
		t.Errorf("problems after stop: %q", p)
	}
}

// TestUIWriteAllowlistOnRoute: the UI's one write is decided on the route
// the request resolves to, so no other spelling of a path reaches another
// operator endpoint or changes a description.
func TestUIWriteAllowlistOnRoute(t *testing.T) {
	f := startUI(t, nil)
	for _, path := range []string{
		"/v1/active%2Fdisable",
		"/v1/hosts/h1%2Fdescription",
		"/v1/hosts/h1/description/",
		"/v1/hosts/h1/description%2F",
		"//v1/active/disable",
		"/v1/active//disable",
		"/v1/hosts//description",
		"/v1/hosts/h1/../../active/disable",
		// Not clean: refused or redirected to the clean path, never served.
		"//v1/hosts/h1/description",
		"/v1/hosts/h1/./description",
	} {
		req, err := http.NewRequest(http.MethodPost, f.base+path, strings.NewReader(`{"description":"x"}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+f.token)
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed && (resp.StatusCode < 300 || resp.StatusCode > 399) {
			t.Errorf("POST %s: %d, want 405 or a redirect", path, resp.StatusCode)
		}
	}
	f.ctl.mu.Lock()
	defer f.ctl.mu.Unlock()
	if len(f.ctl.actors) != 0 {
		t.Errorf("changes made: %v %v", f.ctl.actors, f.ctl.reasons)
	}
}

func TestActivityEndpoint(t *testing.T) {
	f := start(t)
	ctx := context.Background()
	a, err := f.client.Activity(ctx, store.ActivityFilter{Since: t0, Until: t0.Add(5 * time.Hour), Buckets: 5})
	if err != nil || a.StepSeconds != 3600 || len(a.Events) == 0 || len(a.Observations) == 0 || len(a.TopHosts) == 0 {
		t.Fatalf("activity: %v %+v", err, a)
	}
	// Without a range: the last 24 hours up to the daemon's clock, not the
	// wall clock, so a replayed day is shown.
	if a, err = f.client.Activity(ctx, store.ActivityFilter{}); err != nil || !a.Until.Equal(fixtureNow) || a.Until.Sub(a.Since) != 24*time.Hour || len(a.Events) == 0 {
		t.Errorf("default range: %v %v..%v, %d events", err, a.Since, a.Until, len(a.Events))
	}
	for _, q := range []string{"since=yesterday", "buckets=-1", "buckets=100000", "since=2026-10-02T00:00:00Z&until=2026-10-01T00:00:00Z"} {
		resp, err := http.Get("http://" + f.listen + "/v1/activity?" + q)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", q, resp.StatusCode)
		}
	}
}
