package tunnel

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/mihirkanzariya/p2p-tunnel/internal/node"
)

// Client exposes a local port through the P2P network.
type Client struct {
	Node      *node.Node
	LocalAddr string
	Subdomain string
}

// Start registers the tunnel in the DHT and handles incoming streams.
func (c *Client) Start() {
	// Set stream handler — when a gateway sends us an HTTP request.
	c.Node.Host.SetStreamHandler(node.TunnelProtocol, c.handleStream)

	// Advertise in DHT so gateways can find us.
	go func() {
		for {
			c.Node.Advertise(c.Subdomain)
			time.Sleep(30 * time.Second)
		}
	}()
}

func (c *Client) handleStream(s network.Stream) {
	defer s.Close()

	// Read HTTP request from the P2P stream.
	req, err := http.ReadRequest(bufio.NewReader(s))
	if err != nil {
		log.Printf("  [stream] bad request: %v", err)
		return
	}

	fmt.Printf("  %s %s %s (from %s)\n", time.Now().Format("15:04:05"), req.Method, req.URL.Path, s.Conn().RemotePeer().String()[:12])

	// Forward to local server.
	local, err := net.DialTimeout("tcp", c.LocalAddr, 5*time.Second)
	if err != nil {
		writeErr(s, http.StatusBadGateway, "local server unreachable")
		return
	}
	defer local.Close()

	req.Write(local)

	resp, err := http.ReadResponse(bufio.NewReader(local), req)
	if err != nil {
		writeErr(s, http.StatusBadGateway, "bad local response")
		return
	}
	defer resp.Body.Close()

	resp.Write(s)
}

func writeErr(w io.Writer, code int, msg string) {
	body := fmt.Sprintf("%d: %s", code, msg)
	resp := &http.Response{
		StatusCode:    code,
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {"text/plain"}},
		Body:          io.NopCloser(io.LimitReader(bufio.NewReader(io.NopCloser(nil)), 0)),
		ContentLength: int64(len(body)),
	}
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	resp.Body = io.NopCloser(bufio.NewReader(io.NopCloser(nil)))
	fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\n\r\n%s", code, http.StatusText(code), len(body), body)
}
