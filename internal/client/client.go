package client

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/mihirkanzariya/p2p-tunnel/internal/proto"
)

type Client struct {
	RelayAddr string
	Subdomain string
	LocalAddr string
	PublicURL string

	session *yamux.Session
	conn    net.Conn
}

func (c *Client) Connect() error {
	conn, err := net.DialTimeout("tcp", c.RelayAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("cannot reach relay: %w", err)
	}
	c.conn = conn

	proto.SendJSON(conn, proto.Handshake{Subdomain: c.Subdomain})
	var resp proto.HandshakeResponse
	if err := proto.RecvJSON(conn, &resp); err != nil {
		conn.Close()
		return fmt.Errorf("handshake failed: %w", err)
	}
	if !resp.OK {
		conn.Close()
		return fmt.Errorf("rejected: %s", resp.Error)
	}
	c.PublicURL = resp.URL

	cfg := yamux.DefaultConfig()
	cfg.KeepAliveInterval = 10 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	cfg.LogOutput = io.Discard
	session, err := yamux.Server(conn, cfg)
	if err != nil {
		conn.Close()
		return fmt.Errorf("session: %w", err)
	}
	c.session = session
	return nil
}

func (c *Client) Serve() {
	for {
		stream, err := c.session.Accept()
		if err != nil {
			return
		}
		go c.proxy(stream)
	}
}

func (c *Client) proxy(stream net.Conn) {
	defer stream.Close()
	req, err := http.ReadRequest(bufio.NewReader(stream))
	if err != nil {
		return
	}
	log.Printf("  %s %s %s", time.Now().Format("15:04:05"), req.Method, req.URL.Path)

	local, err := net.DialTimeout("tcp", c.LocalAddr, 5*time.Second)
	if err != nil {
		writeErr(stream, http.StatusBadGateway, "local server unreachable at "+c.LocalAddr)
		return
	}
	defer local.Close()
	req.Write(local)
	resp, err := http.ReadResponse(bufio.NewReader(local), req)
	if err != nil {
		writeErr(stream, http.StatusBadGateway, "bad local response")
		return
	}
	defer resp.Body.Close()
	resp.Write(stream)
}

func writeErr(w io.Writer, code int, msg string) {
	body := fmt.Sprintf("%d: %s", code, msg)
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s",
		code, http.StatusText(code), len(body), body)
}

func (c *Client) ConnectWithRetry() error {
	var lastErr error
	for attempt := 1; attempt <= 5; attempt++ {
		err := c.Connect()
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt == 5 {
			return fmt.Errorf("failed after %d attempts: %w", attempt, lastErr)
		}
		wait := time.Duration(attempt*2) * time.Second
		log.Printf("  attempt %d failed: %v (retry in %v)", attempt, err, wait)
		time.Sleep(wait)
	}
	return lastErr
}

func (c *Client) Close() {
	if c.session != nil {
		c.session.Close()
	}
	if c.conn != nil {
		c.conn.Close()
	}
}

// ParseRelayAddr returns host:port for the relay.
// If port not specified, tries common ports.
func ParseRelayAddr(addr string) string {
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimSuffix(addr, "/")
	if !strings.Contains(addr, ":") {
		return addr + ":4443"
	}
	return addr
}
