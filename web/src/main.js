import { createLibp2p } from 'libp2p'
import { noise } from '@chainsafe/libp2p-noise'
import { yamux } from '@chainsafe/libp2p-yamux'
import { webSockets } from '@libp2p/websockets'
import { bootstrap } from '@libp2p/bootstrap'
import { circuitRelayTransport } from '@libp2p/circuit-relay-v2'
import { identify } from '@libp2p/identify'
import { multiaddr } from '@multiformats/multiaddr'

const TUNNEL_PROTOCOL = '/p2p-tunnel/http/1.0.0'

// UI helpers
const logEl = document.getElementById('log')
const dot = document.getElementById('dot')
const statusText = document.getElementById('status-text')
const contentFrame = document.getElementById('content-frame')

function log(msg, type = '') {
  const entry = document.createElement('div')
  entry.className = `log-entry ${type}`
  entry.textContent = `${new Date().toLocaleTimeString()} ${msg}`
  logEl.appendChild(entry)
  logEl.scrollTop = logEl.scrollHeight
}

function setStatus(text, state) {
  statusText.textContent = text
  dot.className = `status-dot ${state}`
}

// Decode the peer address from URL params.
function getPeerAddr() {
  const params = new URLSearchParams(window.location.search)
  const encoded = params.get('addr')
  if (!encoded) return null
  try {
    // Add back padding.
    let padded = encoded
    while (padded.length % 4 !== 0) padded += '='
    return atob(padded.replace(/-/g, '+').replace(/_/g, '/'))
  } catch {
    return null
  }
}

// Create a libp2p node in the browser.
async function createNode() {
  log('Creating browser P2P node...', 'info')

  const node = await createLibp2p({
    transports: [
      webSockets(),
      circuitRelayTransport(),
    ],
    connectionEncrypters: [noise()],
    streamMuxers: [yamux()],
    services: {
      identify: identify(),
    },
    peerDiscovery: [
      bootstrap({
        list: [
          // Public WebSocket bootstrap peers
          '/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN',
          '/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa',
          '/dnsaddr/bootstrap.libp2p.io/p2p/QmbLHAnMoJPWSCR5Zhtx6BHJX9KiKNN6tpvbUcqanj75Nb',
          '/dnsaddr/bootstrap.libp2p.io/p2p/QmcZf59bWwK5XFi76CZX8cbJ4BhTzzA3gU1ZjYZcYW3dwt',
        ]
      })
    ]
  })

  await node.start()
  log(`Node started. Peer ID: ${node.peerId.toString().slice(0, 16)}...`, 'success')
  return node
}

// Connect to the tunnel peer and fetch content.
async function connectAndFetch(node, peerMultiaddr) {
  setStatus('Connecting to tunnel peer...', 'connecting')
  log(`Dialing: ${peerMultiaddr.slice(0, 60)}...`, 'info')

  try {
    const ma = multiaddr(peerMultiaddr)
    const stream = await node.dialProtocol(ma, TUNNEL_PROTOCOL)
    log('Stream opened!', 'success')
    setStatus('Connected — loading content...', 'connecting')

    // Send an HTTP GET request through the P2P stream.
    const httpReq = 'GET / HTTP/1.1\r\nHost: localhost\r\nAccept: text/html\r\nConnection: close\r\n\r\n'
    const encoder = new TextEncoder()
    const decoder = new TextDecoder()

    // Write request.
    const writer = stream.sink
    const reader = stream.source

    // Collect response.
    const reqBytes = encoder.encode(httpReq)

    // Use the stream's sink/source.
    // Write the request.
    await stream.sink(async function* () {
      yield reqBytes
    }())

    // Close our write side to signal we're done sending.
    // Read the response.
    let responseText = ''
    for await (const chunk of reader) {
      responseText += decoder.decode(chunk.subarray())
    }

    log(`Received ${responseText.length} bytes`, 'success')
    setStatus('Connected', 'connected')

    // Parse out the HTTP body.
    const bodyStart = responseText.indexOf('\r\n\r\n')
    if (bodyStart !== -1) {
      const body = responseText.slice(bodyStart + 4)
      displayContent(body)
    } else {
      displayContent(responseText)
    }
  } catch (err) {
    log(`Connection failed: ${err.message}`, 'error')
    setStatus(`Failed: ${err.message}`, 'error')
    // Show manual connect.
    document.getElementById('manual-connect').style.display = 'block'
  }
}

function displayContent(html) {
  contentFrame.style.display = 'block'
  contentFrame.srcdoc = html
}

// Manual connect handler.
window.connectManual = async function () {
  const addr = document.getElementById('addr-input').value.trim()
  if (!addr) return
  if (!window._node) {
    window._node = await createNode()
  }
  await connectAndFetch(window._node, addr)
}

// Main.
async function main() {
  const peerAddr = getPeerAddr()

  if (!peerAddr) {
    setStatus('No peer address provided', 'error')
    log('No ?addr= parameter found in URL.', 'error')
    log('Run "p2p-tunnel http <port>" to get a shareable URL.', 'info')
    document.getElementById('manual-connect').style.display = 'block'
    return
  }

  log(`Target peer: ${peerAddr.slice(0, 80)}...`, 'info')

  try {
    const node = await createNode()
    window._node = node

    // Wait for some peer connections.
    log('Discovering peers...', 'info')
    await new Promise(resolve => setTimeout(resolve, 3000))
    log(`Connected to ${node.getConnections().length} peers`, 'info')

    await connectAndFetch(node, peerAddr)
  } catch (err) {
    log(`Error: ${err.message}`, 'error')
    setStatus('Error', 'error')
    document.getElementById('manual-connect').style.display = 'block'
  }
}

main()
