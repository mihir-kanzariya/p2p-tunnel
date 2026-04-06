package node

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"
)

const (
	// Protocol ID for tunnel HTTP streams.
	TunnelProtocol = "/p2p-tunnel/http/1.0.0"
	// DHT namespace for tunnel registrations.
	TunnelNamespace = "p2p-tunnel"
)

// BootstrapPeers are well-known libp2p peers used for initial discovery.
var BootstrapPeers []multiaddr.Multiaddr

func init() {
	bootstrapAddrs := []string{
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt",
		"/ip4/104.131.131.82/tcp/4001/p2p/QmaCpDMGvV2BGHeYERUEnRQAwe3N8SzbUtfsmvsqQLuvuJ",
	}
	for _, s := range bootstrapAddrs {
		ma, err := multiaddr.NewMultiaddr(s)
		if err == nil {
			BootstrapPeers = append(BootstrapPeers, ma)
		}
	}
}

// Node wraps a libp2p host with DHT and discovery.
type Node struct {
	Host      host.Host
	DHT       *dht.IpfsDHT
	Discovery *routing.RoutingDiscovery
	Ctx       context.Context
	Cancel    context.CancelFunc
}

// New creates a new libp2p node with DHT, relay, and NAT traversal.
// TCP-only to avoid QUIC compatibility issues with newer Go versions.
func New(ctx context.Context, port int) (*Node, error) {
	ctx, cancel := context.WithCancel(ctx)

	listenAddr := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port)

	h, err := libp2p.New(
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		libp2p.Security(noise.ID, noise.New),
		libp2p.NATPortMap(),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(),
		libp2p.EnableNATService(),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create host: %w", err)
	}

	// Every node acts as a relay for others — true P2P, everyone helps.
	relayv2.New(h)

	// DHT in auto-server mode — participates fully when reachable.
	kdht, err := dht.New(ctx, h, dht.Mode(dht.ModeAutoServer))
	if err != nil {
		cancel()
		h.Close()
		return nil, fmt.Errorf("create dht: %w", err)
	}

	if err := kdht.Bootstrap(ctx); err != nil {
		cancel()
		h.Close()
		return nil, fmt.Errorf("bootstrap dht: %w", err)
	}

	// Connect to bootstrap peers in parallel.
	var wg sync.WaitGroup
	for _, pAddr := range BootstrapPeers {
		pi, err := peer.AddrInfoFromP2pAddr(pAddr)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			cctx, ccancel := context.WithTimeout(ctx, 10*time.Second)
			defer ccancel()
			h.Connect(cctx, pi)
		}(*pi)
	}
	wg.Wait()

	disc := routing.NewRoutingDiscovery(kdht)

	return &Node{
		Host:      h,
		DHT:       kdht,
		Discovery: disc,
		Ctx:       ctx,
		Cancel:    cancel,
	}, nil
}

// ConnectToPeer connects directly to a peer by multiaddr string.
// Useful for local/direct peering without relying on DHT propagation.
func (n *Node) ConnectToPeer(addr string) error {
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return err
	}
	pi, err := peer.AddrInfoFromP2pAddr(ma)
	if err != nil {
		return err
	}
	return n.Host.Connect(n.Ctx, *pi)
}

// FullAddrs returns full multiaddrs including peer ID (for sharing with others).
func (n *Node) FullAddrs() []string {
	var addrs []string
	for _, a := range n.Host.Addrs() {
		full := fmt.Sprintf("%s/p2p/%s", a, n.Host.ID())
		addrs = append(addrs, full)
	}
	return addrs
}

// Advertise announces that this node provides a tunnel for the given subdomain.
func (n *Node) Advertise(subdomain string) {
	key := TunnelNamespace + "/" + subdomain
	n.Discovery.Advertise(n.Ctx, key)
}

// FindTunnelPeer discovers the peer providing a tunnel for the given subdomain.
func (n *Node) FindTunnelPeer(subdomain string) (peer.AddrInfo, error) {
	key := TunnelNamespace + "/" + subdomain

	ctx, cancel := context.WithTimeout(n.Ctx, 15*time.Second)
	defer cancel()

	peerCh, err := n.Discovery.FindPeers(ctx, key)
	if err != nil {
		return peer.AddrInfo{}, fmt.Errorf("find peers: %w", err)
	}

	for p := range peerCh {
		if p.ID == n.Host.ID() {
			continue
		}
		if len(p.Addrs) > 0 {
			return p, nil
		}
	}
	return peer.AddrInfo{}, fmt.Errorf("no peer found for subdomain %q", subdomain)
}

// Close shuts down the node.
func (n *Node) Close() {
	n.Cancel()
	n.DHT.Close()
	n.Host.Close()
}

// PeerID returns the peer ID string.
func (n *Node) PeerID() string {
	return n.Host.ID().String()
}

// Addrs returns the listen addresses.
func (n *Node) Addrs() []multiaddr.Multiaddr {
	return n.Host.Addrs()
}
