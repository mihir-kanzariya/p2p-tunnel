package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mihirkanzariya/p2p-tunnel/internal/gateway"
	"github.com/mihirkanzariya/p2p-tunnel/internal/node"
	"github.com/mihirkanzariya/p2p-tunnel/internal/tunnel"
)

const browserPageURL = "https://mihir-kanzariya.github.io/p2p-tunnel"

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
	fmt.Println(`P2P Tunnel — expose localhost to the internet, no central server

Usage:
  p2p-tunnel http <port>                     expose local port via P2P network
  p2p-tunnel http <port> --subdomain myapp   with custom subdomain
  p2p-tunnel gateway                         run HTTP gateway (any public IP node)
  p2p-tunnel version                         show version

How it works:
  1. "p2p-tunnel http 3000" joins the P2P network
  2. Get a browser-accessible URL via relay nodes — no server needed
  3. NAT traversal + hole punching built in
  4. Every node is a relay — no single point of failure`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "http":
		runHTTP(os.Args[2:])
	case "gateway":
		runGateway(os.Args[2:])
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
	subdomain := fs.String("subdomain", "", "Custom subdomain (random if empty)")
	p2pPort := fs.Int("p2p-port", 0, "P2P listen port (random if 0)")
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

	fmt.Printf("\n  p2p-tunnel\n\n")
	fmt.Printf("  Joining P2P network...\n")

	ctx := context.Background()
	n, err := node.New(ctx, *p2pPort)
	if err != nil {
		fmt.Printf("  Error: %v\n", err)
		os.Exit(1)
	}
	defer n.Close()

	fmt.Printf("  Peer ID:  %s\n", n.PeerID()[:16]+"...")

	fmt.Printf("  Connecting to peers...")
	time.Sleep(5 * time.Second)
	peerCount := len(n.Host.Network().Peers())
	fmt.Printf(" %d peers found\n", peerCount)

	tc := &tunnel.Client{
		Node:      n,
		LocalAddr: fmt.Sprintf("localhost:%d", port),
		Subdomain: *subdomain,
	}
	tc.Start()

	fmt.Printf("\n  Tunnel active!\n")
	fmt.Printf("  Subdomain:     %s\n", *subdomain)
	fmt.Printf("  Forwarding to: localhost:%d\n", port)

	// Wait briefly for NAT-mapped addresses to appear.
	time.Sleep(3 * time.Second)

	// Show the best browser URL.
	bestAddr := n.BestBrowserAddr()
	if bestAddr != "" {
		fmt.Printf("\n  Browser URL (share this!):\n")
		fmt.Printf("  -> %s\n", makeBrowserURL(bestAddr))
	}

	// Show all available addresses grouped by type.
	if webrtc := n.WebRTCAddrs(); len(webrtc) > 0 {
		fmt.Printf("\n  WebRTC addresses (direct browser connection):\n")
		for _, a := range webrtc {
			fmt.Printf("    %s\n", a)
		}
	}
	if relay := n.RelayAddrs(); len(relay) > 0 {
		fmt.Printf("\n  Relay addresses:\n")
		for _, a := range relay {
			fmt.Printf("    %s\n", a)
		}
	}

	fmt.Printf("\n  All addresses:\n")
	for _, a := range n.FullAddrs() {
		fmt.Printf("    %s\n", a)
	}

	// Also always show peer ID for DHT discovery.
	fmt.Printf("\n  Peer ID (for DHT discovery): %s\n", n.PeerID())
	fmt.Printf("  Press Ctrl+C to close.\n\n")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	fmt.Println("\n  Tunnel closed.")
}

// makeBrowserURL encodes a multiaddr into a browser-accessible URL.
func makeBrowserURL(multiaddr string) string {
	encoded := base64.URLEncoding.EncodeToString([]byte(multiaddr))
	// Trim padding for cleaner URLs.
	encoded = strings.TrimRight(encoded, "=")
	return fmt.Sprintf("%s/?addr=%s", browserPageURL, url.QueryEscape(encoded))
}

func runGateway(args []string) {
	fs := flag.NewFlagSet("gateway", flag.ExitOnError)
	httpAddr := fs.String("http", ":8080", "HTTP listen address")
	domain := fs.String("domain", "localhost:8080", "Base domain for URLs")
	p2pPort := fs.Int("p2p-port", 4001, "P2P listen port")
	peerAddr := fs.String("peer", "", "Direct peer address to connect to")
	fs.Parse(args)

	fmt.Printf("\n  p2p-tunnel gateway\n\n")
	fmt.Printf("  Joining P2P network...\n")

	ctx := context.Background()
	n, err := node.New(ctx, *p2pPort)
	if err != nil {
		fmt.Printf("  Error: %v\n", err)
		os.Exit(1)
	}
	defer n.Close()

	fmt.Printf("  Peer ID:  %s\n", n.PeerID()[:16]+"...")

	if *peerAddr != "" {
		fmt.Printf("  Connecting to peer directly...\n")
		if err := n.ConnectToPeer(*peerAddr); err != nil {
			fmt.Printf("  Warning: direct connect failed: %v\n", err)
		} else {
			fmt.Printf("  Connected to peer!\n")
		}
	}

	fmt.Printf("  Connecting to bootstrap peers...")
	time.Sleep(5 * time.Second)
	fmt.Printf(" %d peers found\n", len(n.Host.Network().Peers()))

	gw := gateway.New(n, *domain)

	fmt.Printf("\n  Gateway active!\n")
	fmt.Printf("  HTTP:    %s\n", *httpAddr)
	fmt.Printf("  Domain:  *.%s\n", *domain)
	fmt.Printf("  Peer ID: %s\n", n.PeerID())
	fmt.Printf("\n  Routes HTTP requests to tunnel peers via DHT.\n")
	fmt.Printf("  Press Ctrl+C to stop.\n\n")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n  Gateway stopped.")
		n.Close()
		os.Exit(0)
	}()

	if err := gw.ListenAndServe(*httpAddr); err != nil {
		fmt.Printf("  HTTP server error: %v\n", err)
	}
}
