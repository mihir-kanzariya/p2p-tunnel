package main

import (
	"flag"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/mihirkanzariya/p2p-tunnel/internal/client"
	"github.com/mihirkanzariya/p2p-tunnel/internal/relay"
)

// Default public relay — anyone can run their own.
const defaultRelay = "p2p-relay.fly.dev:4443"

var words = []string{
	"alpha", "blue", "calm", "dawn", "echo", "fox", "glow", "haze",
	"ice", "jade", "kite", "luna", "mist", "nova", "oak", "pine",
	"rain", "sky", "tide", "volt", "wave", "zen", "bolt", "dusk",
}

func randomSubdomain() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return fmt.Sprintf("%s-%s-%04d", words[r.Intn(len(words))], words[r.Intn(len(words))], r.Intn(10000))
}

func usage() {
	fmt.Println(`p2p-tunnel — expose localhost to the internet

Usage:
  p2p-tunnel http <port>                  expose local port
  p2p-tunnel http <port> -s myapp         custom subdomain
  p2p-tunnel http <port> -r host:4443     use custom relay
  p2p-tunnel relay                        run your own relay
  p2p-tunnel version

Examples:
  p2p-tunnel http 3000
  p2p-tunnel http 8080 -s demo
  p2p-tunnel relay -d tunnel.example.com`)
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
	subdomain := fs.String("s", "", "Subdomain (random if empty)")
	relayAddr := fs.String("r", defaultRelay, "Relay server address")
	fs.Parse(args[1:])

	if len(args) < 1 {
		fmt.Println("Usage: p2p-tunnel http <port>")
		os.Exit(1)
	}
	port, err := strconv.Atoi(args[0])
	if err != nil || port < 1 || port > 65535 {
		fmt.Println("Error: invalid port")
		os.Exit(1)
	}
	if *subdomain == "" {
		*subdomain = randomSubdomain()
	}

	c := &client.Client{
		RelayAddr: *relayAddr,
		Subdomain: *subdomain,
		LocalAddr: fmt.Sprintf("localhost:%d", port),
	}

	fmt.Printf("\n  p2p-tunnel\n\n")
	fmt.Printf("  Connecting to relay %s ...\n", *relayAddr)

	if err := c.ConnectWithRetry(); err != nil {
		fmt.Printf("  Error: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	fmt.Printf("\n  Public URL:    %s\n", c.PublicURL)
	fmt.Printf("  Forwarding to: localhost:%d\n", port)
	fmt.Printf("  Relay:         %s\n", *relayAddr)
	fmt.Printf("\n  Ready! Press Ctrl+C to close.\n\n")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n  Tunnel closed.")
		c.Close()
		os.Exit(0)
	}()

	c.Serve()
}

func runRelay(args []string) {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	controlAddr := fs.String("control", ":4443", "Control port for tunnel clients")
	httpAddr := fs.String("http", ":8080", "HTTP port for web traffic")
	domain := fs.String("d", "localhost:8080", "Base domain (e.g. tunnel.example.com)")
	fs.Parse(args)

	r := relay.New(*domain)

	// Control listener.
	controlLn, err := net.Listen("tcp", *controlAddr)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	// HTTP server.
	srv := &http.Server{Addr: *httpAddr, Handler: r}
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
		os.Exit(0)
	}()

	for {
		conn, err := controlLn.Accept()
		if err != nil {
			return
		}
		go r.HandleControl(conn)
	}
}
