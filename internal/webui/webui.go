package webui

import (
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
)

//go:embed static/*
var staticFiles embed.FS

// Serve starts a local HTTP server serving the browser UI.
// Returns the URL the user should open.
func Serve() (string, error) {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return "", err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}

	go http.Serve(ln, http.FileServer(http.FS(sub)))
	return fmt.Sprintf("http://%s", ln.Addr().String()), nil
}
