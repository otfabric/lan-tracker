package api_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/otfabric/lan-tracker/internal/api"
)

// get answers a GET on the read-only TCP listener: the status, the
// decoded JSON error and the body.
func get(t *testing.T, base, path string) (int, string, string) {
	t.Helper()
	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	var body struct{ Error string }
	_ = json.Unmarshal(raw, &body)
	return resp.StatusCode, body.Error, string(raw)
}

// TestDHCPAndServiceParameters: the DHCP lease and exchange lists and the
// service list check their parameters, and the services in effect at a
// time are read from the results up to it.
func TestDHCPAndServiceParameters(t *testing.T) {
	f := start(t)
	base := "http://" + f.listen
	for _, tt := range []struct {
		path     string
		status   int
		contains string
	}{
		{"/v1/dhcp/leases?limit=x", http.StatusBadRequest, "limit"},
		{"/v1/dhcp/leases?open=maybe", http.StatusBadRequest, "open"},
		{"/v1/dhcp/exchanges?since=soon", http.StatusBadRequest, "since"},
		{"/v1/dhcp/exchanges?limit=-2", http.StatusBadRequest, "limit"},
		{"/v1/services?at=soon", http.StatusBadRequest, "at"},
		{"/v1/dhcp/leases?open=true&mac=00:1b:1b:aa:bb:01", http.StatusOK, `"ip":"192.168.110.52"`},
		{"/v1/dhcp/exchanges?since=2026-10-01T00:00:00Z", http.StatusOK, `"xid":24158`},
		// Before the first check nothing was known; after it, port 502.
		{"/v1/services?at=2026-10-01T10:00:00Z", http.StatusOK, "[]"},
		{"/v1/services?at=2026-10-01T23:00:00Z&port=502", http.StatusOK, `"last_result":"OPEN"`},
	} {
		code, msg, body := get(t, base, tt.path)
		if code != tt.status || tt.status != http.StatusOK && !strings.Contains(msg, tt.contains) ||
			tt.status == http.StatusOK && !strings.Contains(body, tt.contains) {
			t.Errorf("%s: %d %q %s", tt.path, code, msg, body)
		}
	}
}

// Without an event source (a server started for the UI or a test) the
// stream fails with a JSON error instead of hanging.
func TestStreamWithoutEvents(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "api.sock")
	srv := api.New(api.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx, socket, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = srv.Wait() })
	c := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	resp, err := c.Get("http://x/v1/events/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body struct{ Error string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || resp.StatusCode < 500 || !strings.Contains(body.Error, "event stream unavailable") {
		t.Errorf("stream without events: %d %q %v", resp.StatusCode, body.Error, err)
	}
}

// A socket whose directory cannot be created fails the start with the
// path.
func TestSocketDirectoryNotCreatable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "run")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := api.New(api.Options{}).Start(context.Background(), filepath.Join(file, "api.sock"), "")
	if err == nil || !strings.Contains(err.Error(), "api socket directory") {
		t.Errorf("socket under a file: %v", err)
	}
}
