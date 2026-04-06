package gateway

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/mihirkanzariya/p2p-tunnel/internal/node"
)

// Gateway accepts HTTP traffic and routes it to tunnel peers via the P2P network.
// Any node with a public IP can run a gateway. Multiple gateways = no SPOF.
type Gateway struct {
	Node   *node.Node
	Domain string

	// Cache DHT lookups briefly to avoid hammering DHT on every request.
	mu    sync.RWMutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	peer   peer.AddrInfo
	expiry time.Time
}

func New(n *node.Node, domain string) *Gateway {
	return &Gateway{
		Node:   n,
		Domain: domain,
		cache:  make(map[string]cacheEntry),
	}
}

// ListenAndServe starts the HTTP gateway on the given address.
func (g *Gateway) ListenAndServe(addr string) error {
	srv := &http.Server{
		Addr:         addr,
		Handler:      g,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}
	return srv.ListenAndServe()
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	subdomain := g.extractSubdomain(r.Host)
	if subdomain == "" {
		g.statusPage(w)
		return
	}

	// Find the tunnel peer for this subdomain.
	pi, err := g.findPeer(subdomain)
	if err != nil {
		http.Error(w, fmt.Sprintf("tunnel %q not found in P2P network: %v", subdomain, err), http.StatusBadGateway)
		return
	}

	// Connect to the peer if not already connected.
	if err := g.Node.Host.Connect(r.Context(), pi); err != nil {
		http.Error(w, fmt.Sprintf("cannot reach tunnel peer: %v", err), http.StatusBadGateway)
		return
	}

	// Open a stream to the tunnel peer.
	stream, err := g.Node.Host.NewStream(r.Context(), pi.ID, node.TunnelProtocol)
	if err != nil {
		http.Error(w, fmt.Sprintf("stream to tunnel failed: %v", err), http.StatusBadGateway)
		return
	}
	defer stream.Close()

	// Forward the HTTP request over the P2P stream.
	if err := r.Write(stream); err != nil {
		http.Error(w, "failed to send request through tunnel", http.StatusBadGateway)
		return
	}

	// Read the response from the tunnel peer.
	resp, err := http.ReadResponse(bufio.NewReader(stream), r)
	if err != nil {
		http.Error(w, "failed to read tunnel response", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Write response back to the HTTP client.
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)

	log.Printf("  [gateway] %s %s -> %s (%d)", r.Method, r.URL.Path, subdomain, resp.StatusCode)
}

func (g *Gateway) findPeer(subdomain string) (peer.AddrInfo, error) {
	// Check cache.
	g.mu.RLock()
	if entry, ok := g.cache[subdomain]; ok && time.Now().Before(entry.expiry) {
		g.mu.RUnlock()
		return entry.peer, nil
	}
	g.mu.RUnlock()

	// DHT lookup.
	pi, err := g.Node.FindTunnelPeer(subdomain)
	if err != nil {
		return peer.AddrInfo{}, err
	}

	// Cache for 30 seconds.
	g.mu.Lock()
	g.cache[subdomain] = cacheEntry{peer: pi, expiry: time.Now().Add(30 * time.Second)}
	g.mu.Unlock()

	return pi, nil
}

func (g *Gateway) extractSubdomain(host string) string {
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	parts := strings.SplitN(host, ".", 2)
	if len(parts) >= 2 {
		return parts[0]
	}
	return ""
}

func (g *Gateway) statusPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>P2P Tunnel Gateway</title>
<style>body{font-family:system-ui,sans-serif;max-width:600px;margin:80px auto;padding:0 20px;color:#333}
h1{color:#1a1a1a}.s{background:#f0fdf4;border:1px solid #86efac;border-radius:8px;padding:16px;margin:20px 0}
code{background:#f3f4f6;padding:2px 6px;border-radius:4px;font-size:.9em}</style></head>
<body><h1>P2P Tunnel Gateway</h1>
<div class="s"><strong>Status:</strong> Running<br><strong>Peer ID:</strong> %s<br><strong>Peers connected:</strong> %d</div>
<p>This is a P2P tunnel gateway. Tunnels are discovered via the distributed hash table.</p>
<p>To expose your local server: <code>p2p-tunnel http 3000</code></p>
</body></html>`, g.Node.PeerID()[:16]+"...", len(g.Node.Host.Network().Peers()))
}
