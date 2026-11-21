package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/api"
	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/store"
)

// TestListLimitParameter: the list endpoints accept a limit of at most
// store.MaxListLimit and answer a larger one with 400.
func TestListLimitParameter(t *testing.T) {
	f := start(t)
	base := "http://" + f.listen
	for _, path := range []string{"/v1/observations", "/v1/observations?rollups=true&", "/v1/events", "/v1/dhcp/leases", "/v1/dhcp/exchanges"} {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = ""
		}
		if code, _, _ := get(t, base, fmt.Sprintf("%s%slimit=%d", path, sep, store.MaxListLimit)); code != http.StatusOK {
			t.Errorf("%s limit=%d: %d", path, store.MaxListLimit, code)
		}
		code, msg, _ := get(t, base, path+sep+"limit=1000000000")
		if code != http.StatusBadRequest || !strings.Contains(msg, "limit at most 10000") {
			t.Errorf("%s limit=1000000000: %d %q", path, code, msg)
		}
	}
}

// TestEventTypeFilterCatalogue: the type filter takes every type of the
// event catalogue, by spec and CLI name, and refuses anything else.
func TestEventTypeFilterCatalogue(t *testing.T) {
	f := start(t)
	base := "http://" + f.listen
	for _, s := range events.All() {
		for _, name := range []string{string(s.Type), s.CLIName} {
			if code, msg, _ := get(t, base, "/v1/events?type="+name); code != http.StatusOK {
				t.Errorf("type=%s: %d %q", name, code, msg)
			}
		}
	}
	for _, name := range []string{"NOT_A_TYPE", "ip_added%3Bx", "%22"} {
		if code, msg, _ := get(t, base, "/v1/events?type="+name); code != http.StatusBadRequest || !strings.Contains(msg, "unknown event type") {
			t.Errorf("type=%s: %d %q", name, code, msg)
		}
	}
}

// TestDBCheckSocketOnly: the integrity check is served on the socket only.
func TestDBCheckSocketOnly(t *testing.T) {
	f := start(t)
	if code, _, _ := get(t, "http://"+f.listen, "/v1/db/check"); code != http.StatusNotFound {
		t.Errorf("db check on the TCP listener: %d", code)
	}
	if res, err := f.client.DBCheck(context.Background()); err != nil || !res.OK {
		t.Errorf("db check on the socket: %v %+v", err, res)
	}
}

// TestReadTimeout: reads get a deadline; the event stream does not.
func TestReadTimeout(t *testing.T) {
	defer api.SetReadTimeout(20 * time.Millisecond)()
	var got sync.Map
	h := api.ReadTimeout(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok := r.Context().Deadline()
		got.Store(r.Method+" "+r.URL.Path, ok)
		if ok {
			<-r.Context().Done() // a slow query ends with the deadline
		}
	}))
	srv := &http.Server{Handler: h, ReadHeaderTimeout: time.Second}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()
	for _, c := range []struct {
		method, path string
		deadline     bool
	}{
		{http.MethodGet, "/v1/hosts", true},
		{http.MethodHead, "/v1/status", true},
		{http.MethodGet, "/v1/events/stream", false},
		{http.MethodPost, "/v1/hosts/h1/description", false},
	} {
		req, _ := http.NewRequest(c.method, "http://"+l.Addr().String()+c.path, nil)
		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if v, _ := got.Load(c.method + " " + c.path); v != c.deadline {
			t.Errorf("%s %s: deadline %v, want %v", c.method, c.path, v, c.deadline)
		}
		if c.deadline && time.Since(start) > 5*time.Second {
			t.Errorf("%s %s: not ended by its deadline", c.method, c.path)
		}
	}
}

// counter is an event source that counts its open subscriptions and
// never blocks a publisher.
type counter struct {
	mu   sync.Mutex
	subs map[chan store.Event]bool
	open atomic.Int32
}

func (c *counter) Subscribe(n int) (<-chan store.Event, func()) {
	ch := make(chan store.Event, n)
	c.mu.Lock()
	if c.subs == nil {
		c.subs = map[chan store.Event]bool{}
	}
	c.subs[ch] = true
	c.mu.Unlock()
	c.open.Add(1)
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			c.mu.Lock()
			delete(c.subs, ch)
			c.mu.Unlock()
			c.open.Add(-1)
		})
	}
}

func (c *counter) publish(e store.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for ch := range c.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

func startCounter(t *testing.T) (*counter, string) {
	t.Helper()
	c, _, listen := startCounterSocket(t)
	return c, listen
}

// startCounterSocket is startCounter, also returning the socket path.
func startCounterSocket(t *testing.T) (*counter, string, string) {
	t.Helper()
	c := &counter{}
	srv := api.New(api.Options{Events: c})
	listen := freePort(t)
	socket := filepath.Join(t.TempDir(), "api.sock")
	ctx, cancel := context.WithCancel(context.Background())
	if err := srv.Start(ctx, socket, listen); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return len(srv.Problems()) == 0 })
	t.Cleanup(func() { cancel(); _ = srv.Wait() })
	return c, socket, listen
}

// openStream opens an event stream and returns its status and body.
func openStream(t *testing.T, client *http.Client, listen string) (int, io.ReadCloser) {
	t.Helper()
	resp, err := client.Get("http://" + listen + "/v1/events/stream")
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Body
}

// TestStreamLimit: at most api.MaxStreams streams at once on the loopback
// listener; one more is a 503, a stream on the Unix socket is still served,
// and a stream that ends frees its place.
func TestStreamLimit(t *testing.T) {
	c, socket, listen := startCounterSocket(t)
	var bodies []io.ReadCloser
	defer func() {
		for _, b := range bodies {
			_ = b.Close()
		}
	}()
	tr := &http.Transport{}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	for i := range api.MaxStreams {
		code, body := openStream(t, client, listen)
		bodies = append(bodies, body)
		if code != http.StatusOK {
			t.Fatalf("stream %d: %d", i, code)
		}
	}
	waitUntil(t, func() bool { return c.open.Load() == api.MaxStreams })
	code, msg, _ := get(t, "http://"+listen, "/v1/events/stream")
	if code != http.StatusServiceUnavailable || !strings.Contains(msg, "at most 16 event streams at once on the loopback listeners") {
		t.Errorf("stream over the limit: %d %q", code, msg)
	}
	// The socket is not counted: the operator's watch still gets a stream.
	sctx, scancel := context.WithCancel(context.Background())
	sdone := make(chan error, 1)
	go func() {
		sdone <- api.NewClient(socket, time.Second).Stream(sctx, "", nil, 0, func(store.Event) error { return nil })
	}()
	waitUntil(t, func() bool { return c.open.Load() == api.MaxStreams+1 })
	scancel()
	<-sdone
	waitUntil(t, func() bool { return c.open.Load() == api.MaxStreams })
	_ = bodies[0].Close()
	bodies = bodies[1:]
	waitUntil(t, func() bool { return c.open.Load() == api.MaxStreams-1 })
	code, body := openStream(t, client, listen)
	bodies = append(bodies, body)
	if code != http.StatusOK {
		t.Errorf("stream after one ended: %d", code)
	}
}

// TestStreamStalledReader: a client that stops reading is dropped once a
// write misses its deadline, and its subscription ends.
func TestStreamStalledReader(t *testing.T) {
	defer api.SetStreamWriteTimeout(50 * time.Millisecond)()
	c, listen := startCounter(t)
	conn, err := net.Dial("tcp", listen)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "GET /v1/events/stream HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	// The headers arrive; after that the client reads nothing.
	if line, err := bufio.NewReader(conn).ReadString('\n'); err != nil || !strings.Contains(line, "200") {
		t.Fatalf("stream start: %q %v", line, err)
	}
	waitUntil(t, func() bool { return c.open.Load() == 1 })
	big, _ := json.Marshal(map[string]string{"pad": strings.Repeat("x", 64<<10)})
	deadline := time.Now().Add(20 * time.Second)
	for c.open.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("stalled stream never dropped")
		}
		c.publish(store.Event{Type: "HOST_DISCOVERED", Evidence: big})
		time.Sleep(time.Millisecond)
	}
}
