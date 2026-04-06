package proto

import (
	"encoding/json"
	"fmt"
	"io"
)

// Handshake is sent by the tunnel client to the relay to register a subdomain.
type Handshake struct {
	Subdomain string `json:"subdomain"`
	// Version of the protocol for future compat.
	Version int `json:"version"`
}

// HandshakeResponse is sent by the relay back to the tunnel client.
type HandshakeResponse struct {
	OK      bool   `json:"ok"`
	URL     string `json:"url,omitempty"`
	Error   string `json:"error,omitempty"`
}

// SendJSON writes a JSON message followed by a newline delimiter.
func SendJSON(w io.Writer, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

// RecvJSON reads a newline-delimited JSON message.
func RecvJSON(r io.Reader, v interface{}) error {
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1)
	for {
		n, err := r.Read(tmp)
		if err != nil {
			return err
		}
		if n > 0 {
			if tmp[0] == '\n' {
				break
			}
			buf = append(buf, tmp[0])
		}
	}
	return json.Unmarshal(buf, v)
}
