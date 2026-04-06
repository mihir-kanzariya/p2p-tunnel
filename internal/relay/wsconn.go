package relay

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSConn wraps a gorilla/websocket.Conn to implement net.Conn.
// This lets yamux run over a WebSocket connection.
type WSConn struct {
	ws     *websocket.Conn
	reader io.Reader
	mu     sync.Mutex
}

func NewWSConn(ws *websocket.Conn) *WSConn {
	return &WSConn{ws: ws}
}

func (c *WSConn) Read(b []byte) (int, error) {
	if c.reader == nil {
		_, r, err := c.ws.NextReader()
		if err != nil {
			return 0, err
		}
		c.reader = r
	}
	n, err := c.reader.Read(b)
	if err == io.EOF {
		c.reader = nil
		if n > 0 {
			return n, nil
		}
		return c.Read(b)
	}
	return n, err
}

func (c *WSConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	err := c.ws.WriteMessage(websocket.BinaryMessage, b)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *WSConn) Close() error                       { return c.ws.Close() }
func (c *WSConn) LocalAddr() net.Addr                { return c.ws.LocalAddr() }
func (c *WSConn) RemoteAddr() net.Addr               { return c.ws.RemoteAddr() }
func (c *WSConn) SetDeadline(t time.Time) error      { return nil }
func (c *WSConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *WSConn) SetWriteDeadline(t time.Time) error { return nil }
