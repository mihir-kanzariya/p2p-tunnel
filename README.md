# P2P Tunnel

A peer-to-peer alternative to ngrok. Expose your local servers to the internet through a relay network — no subscription required.

## Quick Start

### Build

```bash
make build
```

This creates two binaries in `bin/`:
- `p2p-relay` — the relay server (runs on a machine with a public IP)
- `p2p-tunnel` — the tunnel client (runs on your local machine)

### Usage

**1. Start the relay server** (on a machine with a public IP, or locally for testing):

```bash
./bin/p2p-relay -control :4443 -http :8080 -domain localhost:8080
```

**2. Start your local server** (any HTTP server):

```bash
python3 -m http.server 3000
```

**3. Expose it through the tunnel:**

```bash
./bin/p2p-tunnel -relay localhost:4443 -subdomain myapp -port 3000
```

**4. Access it:**

```
http://myapp.localhost:8080
```

## Production Deployment

On a VPS with a public IP and a domain (e.g., `tunnel.example.com`):

```bash
# Point *.tunnel.example.com to your VPS IP
./bin/p2p-relay -control :4443 -http :80 -domain tunnel.example.com
```

Then from your local machine:

```bash
./bin/p2p-tunnel -relay your-vps-ip:4443 -subdomain myapp -port 3000
# Access at http://myapp.tunnel.example.com
```

## Architecture

```
Browser → Relay Server (public IP, port 80) → yamux stream → Tunnel Client (behind NAT) → localhost:PORT
```

- **Control channel**: TCP connection from client to relay on port 4443
- **Multiplexing**: yamux allows multiple concurrent HTTP requests over one TCP connection
- **Protocol**: JSON handshake, then raw HTTP forwarding over yamux streams

## CLI Reference

### p2p-relay

| Flag | Default | Description |
|------|---------|-------------|
| `-control` | `:4443` | Address for tunnel client connections |
| `-http` | `:8080` | Address for incoming HTTP traffic |
| `-domain` | `localhost:8080` | Base domain for tunnel URLs |

### p2p-tunnel

| Flag | Default | Description |
|------|---------|-------------|
| `-relay` | `localhost:4443` | Relay server address |
| `-subdomain` | (required) | Subdomain to register |
| `-port` | (required) | Local port to expose |
| `-host` | `localhost` | Local host to forward to |

## License

MIT
