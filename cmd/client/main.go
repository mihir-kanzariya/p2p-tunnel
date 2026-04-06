package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/mihirkanzariya/p2p-tunnel/internal/proto"
)

// ─── Relay Server (embedded) ────────────────────────────────────────────────

type tunnel struct {
	subdomain string
	session   *yamux.Session
	conn      net.Conn
}

type relayServer struct {
	mu      sync.RWMutex
	tunnels map[string]*tunnel
	domain  string
}

func (rs *relayServer) register(subdomain string, t *tunnel) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if _, exists := rs.tunnels[subdomain]; exists {
		return fmt.Errorf("subdomain %q already taken", subdomain)
	}
	rs.tunnels[subdomain] = t
	return nil
}

func (rs *relayServer) remove(subdomain string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if t, ok := rs.tunnels[subdomain]; ok {
		t.session.Close()
		t.conn.Close()
		delete(rs.tunnels, subdomain)
	}
}

func (rs *relayServer) get(subdomain string) (*tunnel, bool) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	t, ok := rs.tunnels[subdomain]
	return t, ok
}

func (rs *relayServer) handleControl(conn net.Conn) {
	defer conn.Close()
	var hs proto.Handshake
	if err := proto.RecvJSON(conn, &hs); err != nil {
		return
	}
	subdomain := strings.TrimSpace(strings.ToLower(hs.Subdomain))
	if subdomain == "" {
		proto.SendJSON(conn, proto.HandshakeResponse{OK: false, Error: "subdomain required"})
		return
	}
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.LogOutput = io.Discard
	session, err := yamux.Client(conn, cfg)
	if err != nil {
		return
	}
	t := &tunnel{subdomain: subdomain, session: session, conn: conn}
	if err := rs.register(subdomain, t); err != nil {
		session.Close()
		proto.SendJSON(conn, proto.HandshakeResponse{OK: false, Error: err.Error()})
		return
	}
	url := fmt.Sprintf("http://%s.%s", subdomain, rs.domain)
	proto.SendJSON(conn, proto.HandshakeResponse{OK: true, URL: url})
	<-session.CloseChan()
	rs.remove(subdomain)
}

func (rs *relayServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}
	parts := strings.SplitN(host, ".", 2)
	subdomain := ""
	if len(parts) >= 2 {
		subdomain = parts[0]
	}
	if subdomain == "" {
		http.Error(w, "no tunnel specified", http.StatusBadRequest)
		return
	}
	t, ok := rs.get(subdomain)
	if !ok {
		http.Error(w, fmt.Sprintf("tunnel %q not found", subdomain), http.StatusBadGateway)
		return
	}
	stream, err := t.session.Open()
	if err != nil {
		rs.remove(subdomain)
		http.Error(w, "tunnel connection lost", http.StatusBadGateway)
		return
	}
	defer stream.Close()
	if err := r.Write(stream); err != nil {
		http.Error(w, "failed to forward request", http.StatusBadGateway)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(stream), r)
	if err != nil {
		http.Error(w, "failed to read tunnel response", http.StatusBadGateway)
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

func startEmbeddedRelay(controlPort, httpPort int) *relayServer {
	rs := &relayServer{
		tunnels: make(map[string]*tunnel),
		domain:  fmt.Sprintf("localhost:%d", httpPort),
	}
	controlLn, err := net.Listen("tcp", fmt.Sprintf(":%d", controlPort))
	if err != nil {
		log.Fatalf("relay control listen: %v", err)
	}
	go func() {
		for {
			conn, err := controlLn.Accept()
			if err != nil {
				return
			}
			go rs.handleControl(conn)
		}
	}()
	srv := &http.Server{Addr: fmt.Sprintf(":%d", httpPort), Handler: rs}
	go srv.ListenAndServe()
	return rs
}

// ─── Tunnel Client ──────────────────────────────────────────────────────────

type tunnelClient struct {
	relayAddr string
	subdomain string
	localAddr string
	session   *yamux.Session
	conn      net.Conn
	publicURL string
}

func (tc *tunnelClient) connect() error {
	conn, err := net.DialTimeout("tcp", tc.relayAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("cannot reach relay: %w", err)
	}
	tc.conn = conn
	proto.SendJSON(conn, proto.Handshake{Subdomain: tc.subdomain, Version: 1})
	var resp proto.HandshakeResponse
	if err := proto.RecvJSON(conn, &resp); err != nil {
		conn.Close()
		return fmt.Errorf("handshake failed: %w", err)
	}
	if !resp.OK {
		conn.Close()
		return fmt.Errorf("rejected: %s", resp.Error)
	}
	tc.publicURL = resp.URL
	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.LogOutput = io.Discard
	session, err := yamux.Server(conn, cfg)
	if err != nil {
		conn.Close()
		return fmt.Errorf("session error: %w", err)
	}
	tc.session = session
	return nil
}

func (tc *tunnelClient) serve() {
	for {
		stream, err := tc.session.Accept()
		if err != nil {
			return
		}
		go tc.proxy(stream)
	}
}

func (tc *tunnelClient) proxy(stream net.Conn) {
	defer stream.Close()
	req, err := http.ReadRequest(bufio.NewReader(stream))
	if err != nil {
		return
	}
	fmt.Printf("  %s %s %s\n", time.Now().Format("15:04:05"), req.Method, req.URL.Path)
	local, err := net.DialTimeout("tcp", tc.localAddr, 5*time.Second)
	if err != nil {
		writeErr(stream, http.StatusBadGateway, "local server not reachable")
		return
	}
	defer local.Close()
	req.Write(local)
	resp, err := http.ReadResponse(bufio.NewReader(local), req)
	if err != nil {
		writeErr(stream, http.StatusBadGateway, "bad response from local server")
		return
	}
	defer resp.Body.Close()
	resp.Write(stream)
}

func writeErr(w io.Writer, code int, msg string) {
	body := fmt.Sprintf("%d %s: %s", code, http.StatusText(code), msg)
	resp := &http.Response{
		StatusCode:    code,
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"text/plain"}, "Content-Length": {strconv.Itoa(len(body))}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	resp.Write(w)
}

func (tc *tunnelClient) close() {
	if tc.session != nil {
		tc.session.Close()
	}
	if tc.conn != nil {
		tc.conn.Close()
	}
}

// ─── Random subdomain ───────────────────────────────────────────────────────

var words = []string{
	"alpha", "blue", "calm", "dawn", "echo", "fox", "glow", "haze",
	"ice", "jade", "kite", "luna", "mist", "nova", "oak", "pine",
	"rain", "sky", "tide", "volt", "wave", "zen", "bolt", "dusk",
	"fern", "gust", "hive", "iris", "jazz", "lark", "moss", "neon",
}

func randomSubdomain() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return fmt.Sprintf("%s-%s-%04d", words[r.Intn(len(words))], words[r.Intn(len(words))], r.Intn(10000))
}

// ─── Main ───────────────────────────────────────────────────────────────────

func usage() {
	fmt.Println(`P2P Tunnel — expose localhost to the internet

Usage:
  p2p-tunnel http <port>                     expose local port (built-in relay)
  p2p-tunnel http <port> --subdomain myapp   with custom subdomain
  p2p-tunnel http <port> --relay host:4443   use remote relay
  p2p-tunnel relay                           run standalone relay server

Examples:
  p2p-tunnel http 3000
  p2p-tunnel http 8080 --subdomain demo
  p2p-tunnel relay --http :80 --control :4443 --domain tunnel.example.com`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "http":
		runHTTP(os.Args[2:])
	case "relay":
		runRelay(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("p2p-tunnel %s\n", version)
	case "help", "--help", "-h":
		usage()
	default:
		// Maybe they passed a port directly: p2p-tunnel 3000
		if _, err := strconv.Atoi(os.Args[1]); err == nil {
			runHTTP(os.Args[1:])
		} else {
			usage()
			os.Exit(1)
		}
	}
}

func runHTTP(args []string) {
	fs := flag.NewFlagSet("http", flag.ExitOnError)
	subdomain := fs.String("subdomain", "", "Custom subdomain (random if empty)")
	relay := fs.String("relay", "", "Remote relay address (uses built-in relay if empty)")
	controlPort := fs.Int("control-port", 4443, "Built-in relay control port")
	httpPort := fs.Int("http-port", 8080, "Built-in relay HTTP port")
	fs.Parse(args[1:])

	if len(args) < 1 {
		fmt.Println("Error: port is required. Usage: p2p-tunnel http <port>")
		os.Exit(1)
	}
	port, err := strconv.Atoi(args[0])
	if err != nil || port < 1 || port > 65535 {
		fmt.Println("Error: invalid port number")
		os.Exit(1)
	}

	if *subdomain == "" {
		*subdomain = randomSubdomain()
	}

	relayAddr := *relay
	embeddedMode := relayAddr == ""
	if embeddedMode {
		relayAddr = fmt.Sprintf("localhost:%d", *controlPort)
		startEmbeddedRelay(*controlPort, *httpPort)
		time.Sleep(100 * time.Millisecond)
	}

	tc := &tunnelClient{
		relayAddr: relayAddr,
		subdomain: *subdomain,
		localAddr: fmt.Sprintf("localhost:%d", port),
	}
	if err := tc.connect(); err != nil {
		log.Fatalf("Error: %v", err)
	}
	defer tc.close()

	fmt.Printf("\n  p2p-tunnel\n\n")
	fmt.Printf("  Public URL:    %s\n", tc.publicURL)
	fmt.Printf("  Forwarding to: localhost:%d\n", port)
	if embeddedMode {
		fmt.Printf("  Relay:         built-in (:%d)\n", *httpPort)
	} else {
		fmt.Printf("  Relay:         %s\n", relayAddr)
	}
	fmt.Printf("\n  Ready! Waiting for requests...\n\n")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n  Tunnel closed.")
		tc.close()
		os.Exit(0)
	}()

	tc.serve()
}

func runRelay(args []string) {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	controlAddr := fs.String("control", ":4443", "Control port")
	httpAddr := fs.String("http", ":8080", "HTTP port")
	domain := fs.String("domain", "localhost:8080", "Base domain")
	fs.Parse(args)

	rs := &relayServer{tunnels: make(map[string]*tunnel), domain: *domain}

	controlLn, err := net.Listen("tcp", *controlAddr)
	if err != nil {
		log.Fatalf("control listen: %v", err)
	}
	srv := &http.Server{Addr: *httpAddr, Handler: rs}
	go srv.ListenAndServe()

	fmt.Printf("\n  p2p-tunnel relay\n\n")
	fmt.Printf("  Control: %s\n", *controlAddr)
	fmt.Printf("  HTTP:    %s\n", *httpAddr)
	fmt.Printf("  Domain:  *.%s\n\n", *domain)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n  Relay stopped.")
		controlLn.Close()
		srv.Close()
		os.Exit(0)
	}()

	for {
		conn, err := controlLn.Accept()
		if err != nil {
			return
		}
		go rs.handleControl(conn)
	}
}
