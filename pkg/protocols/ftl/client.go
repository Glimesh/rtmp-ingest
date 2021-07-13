package ftl

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

type Conn struct {
	Connected bool
	AssignedMediaPort int

	controlConn    net.Conn
	controlScanner *bufio.Scanner
	mediaConn      net.Conn
}

func Dial(addr string, channelID uint32, streamKey []byte) (conn *Conn, err error) {
	tcpConn, err := net.Dial("tcp", addr)
	if err != nil {
		return &Conn{}, err
	}

	scanner := bufio.NewScanner(tcpConn)
	conn = &Conn{controlConn: tcpConn, controlScanner: scanner, mediaConn: nil}

	err = conn.authenticateControlConnection(channelID, streamKey)

	return conn, err
}

func (conn *Conn) Close() {
	conn.Connected = false
	conn.controlConn.Close()
}

func (conn *Conn) authenticateControlConnection(channelID uint32, streamKey []byte) error {
	var err error

	conn.sendControlMessage(requestHmac)

	msg := conn.readControlMessage()
	split := strings.Split(msg, " ")

	hmacHexString := split[1]
	decoded, err := hex.DecodeString(hmacHexString)
	if err != nil {
		return err
	}

	hash := hmac.New(sha512.New, streamKey)
	hash.Write(decoded)

	hmacPayload := hash.Sum(nil)

	fmt.Printf("HMacPayload: %v\n", hmacPayload)

	conn.sendControlMessage(fmt.Sprintf(requestConnect, channelID, hex.EncodeToString(hmacPayload)))
	fmt.Println(conn.readControlMessage())

	// fake for now
	attrs := []string{
		//"ProtocolVersion: 0.9",
		"VendorName: rtmp-ingest",
		"VendorVersion: 1.0",
		"Video: true",
		"VideoCodec: H264",
		"VideoHeight: 720",
		"VideoWidth: 1280",
		"VideoPayloadType: 96",
		fmt.Sprintf(metaVideoIngestSSRC, channelID + 1),
		"Audio: true",
		"AudioCodec: OPUS",
		"AudioPayloadType: 97",
		fmt.Sprintf(metaAudioIngestSSRC, channelID),
	}
	for _, v := range attrs {
		conn.sendControlMessage(v)
	}

	conn.sendControlMessage(requestDot)

	resp := conn.readControlMessage()
	matches := clientMediaPortRegex.FindAllStringSubmatch(resp, 1)
	if len(matches) < 1 {
		conn.Close()
		return err
	}
	fmt.Printf("Matches: %+v\n", matches)
	conn.AssignedMediaPort, err = strconv.Atoi(matches[0][1])
	if err != nil {
		conn.Close()
		return err
	}
	fmt.Printf("Media port %d\n", conn.AssignedMediaPort)

	go conn.heartbeat()
	return nil
}

func (conn *Conn) heartbeat() {
	// Honestly this isn't the best way to do it, we should make command response processing happen async
	// so state is managed on the connection

	// Currently two commands could go out at the same time and we could get the responses confused
	// Assuming the server is handling our commands async anyway
	ticker := time.NewTicker(5 * time.Second)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <- ticker.C:
				conn.sendControlMessage(requestPing)
				if conn.readControlMessage() != "201" {
					conn.Close()
				}
			case <- quit:
				ticker.Stop()
				return
			}
		}
	}()
}

func (conn *Conn) sendControlMessage(message string) error {
	final := message + "\r\n\r\n"
	log.Printf("SEND: %q", final)
	_, err := conn.controlConn.Write([]byte(final))
	return err
}
func (conn *Conn) readControlMessage() string {
	for conn.controlScanner.Scan() {
		recv := conn.controlScanner.Text()
		log.Printf("RECV: %q", recv)
		return recv
	}
	return ""
}

