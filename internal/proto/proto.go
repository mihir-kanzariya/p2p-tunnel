package proto

import (
	"encoding/json"
	"fmt"
	"io"
)

type Handshake struct {
	Subdomain string `json:"subdomain"`
}

type HandshakeResponse struct {
	OK    bool   `json:"ok"`
	URL   string `json:"url,omitempty"`
	Error string `json:"error,omitempty"`
}

func SendJSON(w io.Writer, v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	return err
}

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
