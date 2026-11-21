package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
)

// The web user interface (ADR 0012, docs/API.md "Web UI"): the daemon
// serves the frontend's web build and the read API on a listener of its
// own (loopback by default; ui.listen may name any local address). Every /v1/ request needs the bearer token; the page takes it
// from the URL fragment, which a browser never sends to a server. The one
// change the UI may make is a host's description, recorded with the actor
// "ui".

// UIActor is the actor recorded for a change made through the web UI.
const UIActor = "ui"

// UIOptions configures the web UI listener.
type UIOptions struct {
	Listen string // host:port; the host is localhost or an IP address (config.validator.ui)
	Token  string // the bearer token (LoadUIToken)
	// Addrs lists the box's addresses, for the URLs of a listener on every
	// address (0.0.0.0 or ::); nil uses net.InterfaceAddrs.
	Addrs func() ([]net.Addr, error)
	// Assets is the web build, with index.html at its root. Without one
	// (a binary built without `make frontend`) the page says so.
	Assets fs.FS
}

// UIInfo is the answer of GET /v1/ui (socket only): where the UI listens
// and the URL to open, with the token in its fragment. URLs lists every
// URL the UI answers at (one per address for a listener on 0.0.0.0 or ::),
// URL the first of them.
type UIInfo struct {
	Enabled bool     `json:"enabled"`
	Listen  string   `json:"listen,omitempty"`
	URL     string   `json:"url,omitempty"`
	URLs    []string `json:"urls,omitempty"`
}

// uiCSP lets the page load and call its own origin and nothing else.
const uiCSP = "default-src 'self'; connect-src 'self'; img-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// uiTokenBytes is the token's entropy before encoding.
const uiTokenBytes = 32

// LoadUIToken reads the UI token from path, creating it (mode 0600) with a
// new random token when it does not exist. Removing the file and
// restarting the daemon replaces the token. A symbolic link at path is
// refused; a file other users could read (mode wider than 0600) or that
// another user owns is replaced with a new token, logged at info.
func LoadUIToken(path string, log *slog.Logger) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return newUIToken(path)
	}
	if errors.Is(err, syscall.ELOOP) {
		return "", fmt.Errorf("ui token %s: is a symbolic link; refusing to follow it", path)
	}
	if err != nil {
		return "", fmt.Errorf("ui token %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("ui token %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("ui token %s: not a regular file", path)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); fi.Mode().Perm()&^0o600 != 0 || ok && int64(st.Uid) != int64(os.Geteuid()) {
		// The token is a password: one others could have read is spent.
		if log != nil {
			log.Info("ui token file was readable by others or owned by another user; replaced it with a new token",
				"path", path, "mode", fi.Mode().Perm().String())
		}
		if err := os.Remove(path); err != nil {
			return "", fmt.Errorf("ui token %s: %w", path, err)
		}
		return newUIToken(path)
	}
	b, err := io.ReadAll(io.LimitReader(f, 1024))
	if err != nil {
		return "", fmt.Errorf("ui token %s: %w", path, err)
	}
	tok := strings.TrimSpace(string(b))
	if len(tok) < 32 || strings.ContainsAny(tok, " \t\r\n") {
		return "", fmt.Errorf("ui token %s: not a token (remove the file to create a new one)", path)
	}
	return tok, nil
}

// newUIToken creates path (mode 0600, never through a symbolic link) with
// a new random token.
func newUIToken(path string) (string, error) {
	raw := make([]byte, uiTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("ui token: %w", err)
	}
	tok := base64.RawURLEncoding.EncodeToString(raw)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", fmt.Errorf("ui token %s: %w", path, err)
	}
	_, werr := writeUIToken(f, tok+"\n")
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		// A partial file would be read back as "not a token" at the next
		// start; without it, that start creates a new one.
		_ = os.Remove(path)
		return "", fmt.Errorf("ui token %s: %w", path, err)
	}
	return tok, nil
}

// writeUIToken writes the token to its new file; a test replaces it to
// make the write fail.
var writeUIToken = (*os.File).WriteString

func (s *Server) uiInfo(w http.ResponseWriter, _ *http.Request) {
	ui := s.o.UI
	if ui == nil {
		writeJSON(w, http.StatusOK, UIInfo{})
		return
	}
	info := UIInfo{Enabled: true, Listen: ui.Listen}
	for _, a := range uiAddrs(ui) {
		info.URLs = append(info.URLs, "http://"+a+"/#token="+ui.Token)
	}
	info.URL = info.URLs[0]
	writeJSON(w, http.StatusOK, info)
}

// uiAddrs lists the host:port addresses the UI answers at: the listener's
// own, or for one on every address (0.0.0.0, ::) loopback first and then
// each global unicast address of the box, of the family the listener
// takes (on Linux :: takes IPv4 too).
func uiAddrs(ui *UIOptions) []string {
	host, port, err := net.SplitHostPort(ui.Listen)
	ip, perr := netip.ParseAddr(host)
	if err != nil || perr != nil || !ip.IsUnspecified() {
		return []string{ui.Listen}
	}
	out := []string{net.JoinHostPort("127.0.0.1", port)}
	if ip.Is6() {
		out = append(out, net.JoinHostPort("::1", port))
	}
	list := ui.Addrs
	if list == nil {
		list = net.InterfaceAddrs
	}
	addrs, _ := list() // without the list, loopback is still right
	var v6 []string
	for _, a := range addrs {
		p, err := netip.ParsePrefix(a.String())
		if err != nil || !p.Addr().IsGlobalUnicast() {
			continue
		}
		switch a := p.Addr().Unmap(); {
		case a.Is4():
			out = append(out, net.JoinHostPort(a.String(), port))
		case ip.Is6():
			v6 = append(v6, net.JoinHostPort(a.String(), port))
		}
	}
	return append(out, v6...)
}

// loopbackHost reports whether the request's Host is a loopback name or
// address (a web page could otherwise reach a listener through DNS
// rebinding).
func loopbackHost(r *http.Request) bool {
	host := requestHost(r)
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}

func requestHost(r *http.Request) string {
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		return h
	}
	return r.Host
}

// uiHostCheck returns the Host check of the UI listener. On loopback it is
// loopbackHost. On another address it also takes any IP address and the
// box's own hostname (and hostname.local): a page served under another
// name cannot reach the UI through DNS rebinding, since its Host is that
// name, while a browser on the LAN uses the address or the box's name.
func uiHostCheck(listen string, hostname func() (string, error)) func(*http.Request) bool {
	host, _, _ := net.SplitHostPort(listen)
	if ip := net.ParseIP(host); host == "localhost" || ip != nil && ip.IsLoopback() {
		return loopbackHost
	}
	var names []string
	if n, err := hostname(); err == nil && n != "" {
		n = strings.ToLower(strings.TrimSuffix(n, "."))
		names = append(names, n, n+".local")
	}
	return func(r *http.Request) bool {
		h := strings.ToLower(strings.TrimSuffix(requestHost(r), "."))
		return loopbackHost(r) || net.ParseIP(h) != nil || slices.Contains(names, h)
	}
}

// uiHostname is the box's name for uiHostCheck; a test replaces it.
var uiHostname = os.Hostname

// uiHandler serves the web UI listener.
func (s *Server) uiHandler() http.Handler {
	ui := s.o.UI
	assets := ui.Assets
	if assets != nil {
		if _, err := fs.Stat(assets, "index.html"); err != nil {
			assets = nil
		}
	}
	hostOK := uiHostCheck(ui.Listen, uiHostname)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", uiCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if !hostOK(r) {
			writeJSON(w, http.StatusForbidden, errorBody{Error: "Host must be an address or this box's name"})
			return
		}
		if r.URL.Path == "/v1" || strings.HasPrefix(r.URL.Path, "/v1/") {
			s.uiAPI(w, r, ui.Token)
			return
		}
		uiAssets(w, r, assets)
	})
}

// uiAPI serves the API to the page: reads, and a host's description.
func (s *Server) uiAPI(w http.ResponseWriter, r *http.Request, token string) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
		h.Set("WWW-Authenticate", `Bearer realm="lan-tracker"`)
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: "missing or wrong token: open the URL `lan-tracker ui url` prints", Code: "token"})
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
	case http.MethodPost:
		// The decision is made on the route the request resolves to, the
		// same one that serves it, so no spelling of the path (escaped
		// slashes, doubled slashes, a trailing slash) reaches another
		// handler.
		if _, pattern := s.mux.Handler(r); pattern != descriptionPattern {
			h.Set("Allow", "GET, HEAD")
			writeJSON(w, http.StatusMethodNotAllowed, errorBody{Error: "the web UI only reads and changes host descriptions"})
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), actorKey{}, UIActor))
	default:
		h.Set("Allow", "GET, HEAD")
		writeJSON(w, http.StatusMethodNotAllowed, errorBody{Error: r.Method + " is not allowed"})
		return
	}
	readTimeout(s.mux).ServeHTTP(w, r)
}

// uiPlaceholder is the page of a binary built without the frontend.
const uiPlaceholder = `<!doctype html><meta charset="utf-8"><title>LAN Tracker</title>
<p>This lan-tracker binary was built without its web UI. Build it with <code>make package</code> (or <code>make frontend build</code>).</p>`

// uiAssets serves the web build. A path that names no file gets
// index.html, so the page can be reloaded on any view; index.html is never
// cached, files under assets/ carry a content hash and are cached for good.
func uiAssets(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if assets == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(uiPlaceholder))
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" || name == "index.html" || !isFile(assets, name) {
		// A missing asset is an error, not a page.
		if strings.HasPrefix(name, "assets/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, assets, "index.html")
		return
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, assets, name)
}

func isFile(assets fs.FS, name string) bool {
	info, err := fs.Stat(assets, name)
	return err == nil && !info.IsDir() && !strings.HasPrefix(path.Base(name), ".")
}
