package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/mihirkanzariya/p2p-tunnel/internal/gateway"
	"github.com/mihirkanzariya/p2p-tunnel/internal/node"
	"github.com/mihirkanzariya/p2p-tunnel/internal/tunnel"
)

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
  1. "p2p-tunnel http 3000" joins the P2P network and advertises your tunnel
  2. "p2p-tunnel gateway" on any public-IP machine accepts HTTP and routes via DHT
  3. Multiple gateways = no single point of failure
  4. NAT traversal + hole punching built in`)
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

	fmt.Printf("  Peer ID:       %s\n", n.PeerID()[:16]+"...")
	fmt.Printf("  Listening on:  ")
	for _, a := range n.Addrs() {
		fmt.Printf("%s ", a)
	}
	fmt.Println()

	// Wait a moment for DHT to populate.
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
	fmt.Printf("  Peer ID:       %s\n", n.PeerID())
	fmt.Printf("\n  Connect address (share with gateway):\n")
	for _, a := range n.FullAddrs() {
		fmt.Printf("    %s\n", a)
	}
	fmt.Printf("\n  Any gateway node can now route traffic to this tunnel.\n")
	fmt.Printf("  Press Ctrl+C to close.\n\n")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	fmt.Println("\n  Tunnel closed.")
}

func runGateway(args []string) {
	fs := flag.NewFlagSet("gateway", flag.ExitOnError)
	httpAddr := fs.String("http", ":8080", "HTTP listen address")
	domain := fs.String("domain", "localhost:8080", "Base domain for URLs")
	p2pPort := fs.Int("p2p-port", 4001, "P2P listen port")
	peerAddr := fs.String("peer", "", "Direct peer address to connect to (optional, skips DHT wait)")
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

	fmt.Printf("  Peer ID:       %s\n", n.PeerID()[:16]+"...")

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
