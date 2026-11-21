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
	"os"
	"path"
	"strings"
	"syscall"
)

// The web user interface (ADR 0012, docs/API.md "Web UI"): the daemon
// serves the frontend's web build and the read API on a loopback listener
// of its own. Every /v1/ request needs the bearer token; the page takes it
// from the URL fragment, which a browser never sends to a server. The one
// change the UI may make is a host's description, recorded with the actor
// "ui".

// UIActor is the actor recorded for a change made through the web UI.
const UIActor = "ui"

// UIOptions configures the web UI listener.
type UIOptions struct {
	Listen string // loopback host:port
	Token  string // the bearer token (LoadUIToken)
	// Assets is the web build, with index.html at its root. Without one
	// (a binary built without `make frontend`) the page says so.
	Assets fs.FS
}

// UIInfo is the answer of GET /v1/ui (socket only): where the UI listens
// and the URL to open, with the token in its fragment.
type UIInfo struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen,omitempty"`
	URL     string `json:"url,omitempty"`
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
	writeJSON(w, http.StatusOK, UIInfo{Enabled: true, Listen: ui.Listen, URL: "http://" + ui.Listen + "/#token=" + ui.Token})
}

// loopbackHost reports whether the request's Host is a loopback name or
// address (a web page could otherwise reach a listener through DNS
// rebinding).
func loopbackHost(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return host == "localhost" || ip != nil && ip.IsLoopback()
}

// uiHandler serves the web UI listener.
func (s *Server) uiHandler() http.Handler {
	ui := s.o.UI
	assets := ui.Assets
	if assets != nil {
		if _, err := fs.Stat(assets, "index.html"); err != nil {
			assets = nil
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", uiCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if !loopbackHost(r) {
			writeJSON(w, http.StatusForbidden, errorBody{Error: "Host must be a loopback address"})
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
