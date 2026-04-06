package main

import (
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	fmt.Println("Dialing wss://p2p-relay.onrender.com/_tunnel/connect ...")
	conn, resp, err := d.Dial("wss://p2p-relay.onrender.com/_tunnel/connect", nil)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		if resp != nil {
			fmt.Printf("HTTP Status: %d\n", resp.StatusCode)
		}
		return
	}
	fmt.Printf("Connected! Status: %d\n", resp.StatusCode)

	// Send handshake JSON
	msg := []byte("{\"subdomain\":\"debugtest\"}\n")
	err = conn.WriteMessage(websocket.BinaryMessage, msg)
	fmt.Printf("Write err: %v\n", err)

	// Read response
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, data, err := conn.ReadMessage()
	fmt.Printf("Response: %s, err: %v\n", string(data), err)
	conn.Close()
}
