package node

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
	libp2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"
	"github.com/multiformats/go-multiaddr"
)

const (
	TunnelProtocol  = "/p2p-tunnel/http/1.0.0"
	TunnelNamespace = "p2p-tunnel"
)

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

type Node struct {
	Host      host.Host
	DHT       *dht.IpfsDHT
	Discovery *routing.RoutingDiscovery
	Ctx       context.Context
	Cancel    context.CancelFunc
}

// New creates a libp2p node with TCP + WebSocket transports, DHT, relay, and AutoRelay.
func New(ctx context.Context, port int) (*Node, error) {
	ctx, cancel := context.WithCancel(ctx)

	tcpAddr := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port)
	wsPort := port + 1
	if port == 0 {
		wsPort = 0
	}
	wsAddr := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d/ws", wsPort)

	// Peer source for AutoRelay — uses connected peers as relay candidates.
	// This runs after DHT bootstrap, so peers will be available.
	peerSource := func(ctx context.Context, numPeers int) <-chan peer.AddrInfo {
		ch := make(chan peer.AddrInfo, numPeers)
		go func() {
			defer close(ch)
			// Wait for the host to be set up and have connections.
			time.Sleep(15 * time.Second)
			// Offer connected peers as potential relays.
			for _, p := range BootstrapPeers {
				pi, err := peer.AddrInfoFromP2pAddr(p)
				if err != nil {
					continue
				}
				select {
				case ch <- *pi:
				case <-ctx.Done():
					return
				}
			}
		}()
		return ch
	}

	h, err := libp2p.New(
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(ws.New),
		libp2p.ListenAddrStrings(tcpAddr, wsAddr),
		libp2p.Security(libp2ptls.ID, libp2ptls.New),
		libp2p.Security(noise.ID, noise.New),
		libp2p.NATPortMap(),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(),
		libp2p.EnableNATService(),
		libp2p.EnableAutoRelayWithPeerSource(peerSource, autorelay.WithNumRelays(2)),
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create host: %w", err)
	}

	// Act as a relay for others — every node helps the network.
	relayv2.New(h)

	// DHT for peer/tunnel discovery.
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

	// Connect to bootstrap peers.
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

func (n *Node) FullAddrs() []string {
	var addrs []string
	for _, a := range n.Host.Addrs() {
		full := fmt.Sprintf("%s/p2p/%s", a, n.Host.ID())
		addrs = append(addrs, full)
	}
	return addrs
}

// RelayAddrs returns circuit relay addresses (if any) that browsers can connect through.
func (n *Node) RelayAddrs() []string {
	var relayAddrs []string
	for _, a := range n.Host.Addrs() {
		s := a.String()
		if strings.Contains(s, "p2p-circuit") {
			full := fmt.Sprintf("%s/p2p/%s", a, n.Host.ID())
			relayAddrs = append(relayAddrs, full)
		}
	}
	return relayAddrs
}

// WsAddrs returns all WebSocket addresses.
func (n *Node) WsAddrs() []string {
	var wsAddrs []string
	for _, a := range n.Host.Addrs() {
		s := a.String()
		if strings.Contains(s, "/ws") {
			full := fmt.Sprintf("%s/p2p/%s", a, n.Host.ID())
			wsAddrs = append(wsAddrs, full)
		}
	}
	return wsAddrs
}

// PublicWsAddrs returns WebSocket addresses that are publicly routable (not 127.0.0.1 or 10.x).
func (n *Node) PublicWsAddrs() []string {
	var addrs []string
	for _, a := range n.Host.Addrs() {
		s := a.String()
		if !strings.Contains(s, "/ws") {
			continue
		}
		if strings.HasPrefix(s, "/ip4/127.") || strings.HasPrefix(s, "/ip4/10.") ||
			strings.HasPrefix(s, "/ip4/192.168.") || strings.HasPrefix(s, "/ip4/172.") {
			continue
		}
		full := fmt.Sprintf("%s/p2p/%s", a, n.Host.ID())
		addrs = append(addrs, full)
	}
	return addrs
}

func (n *Node) Advertise(subdomain string) {
	key := TunnelNamespace + "/" + subdomain
	n.Discovery.Advertise(n.Ctx, key)
}

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

func (n *Node) Close() {
	n.Cancel()
	n.DHT.Close()
	n.Host.Close()
}

func (n *Node) PeerID() string {
	return n.Host.ID().String()
}

func (n *Node) Addrs() []multiaddr.Multiaddr {
	return n.Host.Addrs()
}
