// plain-rtmp-handshake.go provides a quick way to test a basic RTMP handshake
// go run plain-rtmp-handshake.go
//
// Old Plain Handshake
//	This type of handshake is described everywhere (but not working with all servers):
//	1. client sends 0x03 and 1536 bytes of random data
//	2. server sends 0x03 and 1536 bytes of its own random data
//	3. server sends 1536 bytes of data, containing exactly what client has sent
//	4. client sends 1536 bytes of server-generated data back to server
package main

import (
	"bufio"
	"crypto/rand"
	"fmt"
	"io"
	"net"
)

func main() {
	token := make([]byte, 1536)
	rand.Read(token)

	wrongResponse := make([]byte, 1536)
	rand.Read(wrongResponse)

	conn, err := net.Dial("tcp", "localhost:1935")
	if err != nil {
		panic(err)
	}
	reader := bufio.NewReader(conn)

	// Write 0x03 and token
	conn.Write([]byte{0x03})
	conn.Write(token)

	// Check status response
	status := make([]byte, 1)
	_, err = reader.Read(status)
	if err != nil {
		panic(err)
	}
	fmt.Printf("First Res: %#v\n", status)

	// Generate handshake
	handshake := make([]byte, 1536)
	_, err = io.ReadFull(conn, handshake)
	if err != nil {
		panic(err)
	}
	conn.Write(handshake)
	// Uncomment this if you want the handshake to fail
	// conn.Write(wrongResponse)
	_, err = reader.Read(status)
	if err != nil {
		panic(err)
	}
	fmt.Printf("Second Res: %#v\n", status)
}
