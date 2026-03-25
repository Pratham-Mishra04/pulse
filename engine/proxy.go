package engine

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync/atomic"

	"github.com/Pratham-Mishra04/pulse/internal/log"
)

// ReverseProxy owns the public listener and atomically swaps the backend
// process behind it. The zero value is not usable; use NewReverseProxy.
//
// Traffic flow:
//
//	client → ReverseProxy(:8080) → process(:dynamic)
//
// SwapBackend replaces the backend in one atomic store — there is no window
// where the listener is down or returning errors due to the swap itself.
type ReverseProxy struct {
	addr    string
	backend atomic.Pointer[httputil.ReverseProxy] // nil until first SwapBackend
	downMsg atomic.Pointer[string]                // non-nil when backend crashed
	server  *http.Server
	log     *log.Logger
}

// NewReverseProxy creates a ReverseProxy that will listen on addr (e.g. ":8080").
// Call Start() to begin accepting connections.
func NewReverseProxy(cfg ProxyConfig, l *log.Logger) *ReverseProxy {
	p := &ReverseProxy{
		addr: cfg.Addr,
		log:  l,
	}
	p.server = &http.Server{
		Addr:         cfg.Addr,
		Handler:      p,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}
	return p
}

// Start binds the public address and begins serving in a background goroutine.
// Returns an error if the address cannot be bound.
func (p *ReverseProxy) Start() error {
	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		return fmt.Errorf("proxy: failed to bind %s: %w", p.addr, err)
	}
	go p.server.Serve(ln) //nolint:errcheck // Serve always returns a non-nil error on close
	return nil
}

// SwapBackend atomically points the proxy at http://127.0.0.1:<port>.
// Any request that arrives after this call is forwarded to the new backend;
// requests already in-flight to the old backend continue uninterrupted.
// Also clears any crash message set by MarkCrashed.
//
// Callers must serialize SwapBackend and MarkCrashed (e.g. via proxyMu in
// engine.go) to avoid leaving backend and downMsg in an inconsistent state.
func (p *ReverseProxy) SwapBackend(port int) {
	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		p.log.Error(fmt.Sprintf("proxy error: %v", err))
		http.Error(w, "service unavailable", http.StatusBadGateway)
	}
	p.downMsg.Store(nil)
	p.backend.Store(rp)
}

// MarkCrashed clears the backend and stores msg so that ServeHTTP returns
// a 503 with the crash message instead of the generic "service starting...".
//
// Callers must serialize MarkCrashed and SwapBackend (e.g. via proxyMu in
// engine.go) to avoid leaving backend and downMsg in an inconsistent state.
func (p *ReverseProxy) MarkCrashed(msg string) {
	p.backend.Store(nil)
	p.downMsg.Store(&msg)
}

// Stop gracefully shuts down the HTTP server, waiting for in-flight requests
// to complete up to the deadline in ctx.
func (p *ReverseProxy) Stop(ctx context.Context) error {
	return p.server.Shutdown(ctx)
}

// Addr returns the address the proxy is listening on.
func (p *ReverseProxy) Addr() string {
	return p.addr
}

// ServeHTTP implements http.Handler. It reads the current backend atomically
// on every request so SwapBackend takes effect with zero downtime.
func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rp := p.backend.Load()
	if rp == nil {
		msg := "service starting..."
		if down := p.downMsg.Load(); down != nil {
			msg = *down
		}
		http.Error(w, msg, http.StatusServiceUnavailable)
		return
	}
	rp.ServeHTTP(w, r)
}

// freePort asks the OS for an available TCP port on loopback by binding :0,
// then immediately closing the listener and returning the port number.
// There is a small TOCTOU window between close and process bind, but this is
// harmless for local dev ports where conflicts are extremely unlikely.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("failed to find a free port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}
