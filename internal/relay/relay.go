package relay

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/mihirkanzariya/p2p-tunnel/internal/proto"
)

type tunnel struct {
	subdomain string
	session   *yamux.Session
}

type Relay struct {
	mu       sync.RWMutex
	tunnels  map[string]*tunnel
	Domain   string
	upgrader websocket.Upgrader
}

func New(domain string) *Relay {
	return &Relay{
		tunnels: make(map[string]*tunnel),
		Domain:  domain,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// HandleControl handles raw TCP control connections (for local/self-hosted relays).
func (r *Relay) HandleControl(conn net.Conn) {
	defer conn.Close()
	r.setupTunnel(conn)
}

// handleWsControl handles WebSocket control connections (for cloud platforms with one port).
func (r *Relay) handleWsControl(w http.ResponseWriter, req *http.Request) {
	wsConn, err := r.upgrader.Upgrade(w, req, nil)
	if err != nil {
		log.Printf("[relay] ws upgrade error: %v", err)
		return
	}
	// Wrap WebSocket as net.Conn for yamux.
	conn := NewWSConn(wsConn)
	r.setupTunnel(conn)
}

func (r *Relay) setupTunnel(conn net.Conn) {
	var hs proto.Handshake
	if err := proto.RecvJSON(conn, &hs); err != nil {
		conn.Close()
		return
	}
	sub := strings.TrimSpace(strings.ToLower(hs.Subdomain))
	if sub == "" {
		proto.SendJSON(conn, proto.HandshakeResponse{OK: false, Error: "subdomain required"})
		conn.Close()
		return
	}
	for _, c := range sub {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			proto.SendJSON(conn, proto.HandshakeResponse{OK: false, Error: "invalid subdomain"})
			conn.Close()
			return
		}
	}

	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.LogOutput = io.Discard
	session, err := yamux.Client(conn, cfg)
	if err != nil {
		conn.Close()
		return
	}

	r.mu.Lock()
	if _, exists := r.tunnels[sub]; exists {
		r.mu.Unlock()
		session.Close()
		proto.SendJSON(conn, proto.HandshakeResponse{OK: false, Error: fmt.Sprintf("%q already taken", sub)})
		return
	}
	r.tunnels[sub] = &tunnel{subdomain: sub, session: session}
	r.mu.Unlock()

	url := fmt.Sprintf("https://%s.%s", sub, r.Domain)
	proto.SendJSON(conn, proto.HandshakeResponse{OK: true, URL: url})
	log.Printf("[relay] + %s (%s)", sub, conn.RemoteAddr())

	<-session.CloseChan()

	r.mu.Lock()
	delete(r.tunnels, sub)
	r.mu.Unlock()
	log.Printf("[relay] - %s", sub)
}

func (r *Relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// WebSocket control path: /_tunnel/connect
	if req.URL.Path == "/_tunnel/connect" {
		r.handleWsControl(w, req)
		return
	}

	host := req.Host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	parts := strings.SplitN(host, ".", 2)
	sub := ""
	if len(parts) >= 2 {
		sub = parts[0]
	}

	if sub == "" {
		r.statusPage(w)
		return
	}

	r.mu.RLock()
	t, ok := r.tunnels[sub]
	r.mu.RUnlock()

	if !ok {
		http.Error(w, fmt.Sprintf("tunnel %q not found", sub), http.StatusBadGateway)
		return
	}

	stream, err := t.session.Open()
	if err != nil {
		r.mu.Lock()
		delete(r.tunnels, sub)
		r.mu.Unlock()
		http.Error(w, "tunnel disconnected", http.StatusBadGateway)
		return
	}
	defer stream.Close()

	if err := req.Write(stream); err != nil {
		http.Error(w, "forward failed", http.StatusBadGateway)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(stream), req)
	if err != nil {
		http.Error(w, "tunnel response failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (r *Relay) statusPage(w http.ResponseWriter) {
	r.mu.RLock()
	n := len(r.tunnels)
	r.mu.RUnlock()
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><title>P2P Tunnel Relay</title>
<style>body{font-family:system-ui;max-width:500px;margin:80px auto;padding:0 20px;color:#333}
code{background:#f3f4f6;padding:2px 6px;border-radius:4px}.s{background:#f0fdf4;border:1px solid #86efac;border-radius:8px;padding:16px;margin:20px 0}</style></head>
<body><h1>P2P Tunnel Relay</h1>
<div class="s"><b>Active tunnels:</b> %d</div>
<p>Expose your local server:</p>
<pre><code>p2p-tunnel http 3000</code></pre>
</body></html>`, n)
}
