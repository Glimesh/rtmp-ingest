package ftl

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

type Conn struct {
	AssignedMediaPort int

	channelId ChannelID

	controlConn      net.Conn
	controlConnected bool
	controlScanner   *bufio.Reader
	mediaConn        net.Conn

	lastPing         time.Time
	failedHeartbeats int
	quitTimer        chan bool
}

func Dial(addr string, channelID ChannelID, streamKey []byte) (conn *Conn, err error) {
	tcpConn, err := net.Dial("tcp", addr)
	if err != nil {
		return &Conn{}, err
	}

	scanner := bufio.NewReader(tcpConn)
	conn = &Conn{
		controlConn:    tcpConn,
		controlScanner: scanner,
		mediaConn:      nil,
		channelId:      channelID,
		quitTimer:      make(chan bool, 1),
	}

	if err = conn.sendAuthentication(channelID, streamKey); err != nil {
		conn.Close()
		return conn, err
	}
	time.Sleep(time.Second)
	if err = conn.sendMetadataBatch(); err != nil {
		conn.Close()
		return conn, err
	}
	time.Sleep(time.Second)
	if err = conn.sendMediaStart(); err != nil {
		conn.Close()
		return conn, err
	}

	// After we're finally connected, make sure we stay alive
	go conn.heartbeat()

	return conn, err
}

//func (conn *Conn) eternalRead() {
//	var buf [1024]byte
//	for {
//		n, err := conn.controlConn.Read(buf[0:])
//		if err != nil {
//
//		}
//	}
//}

func (conn *Conn) Close() error {
	conn.controlConnected = false
	conn.quitTimer <- true

	conn.sendControlMessage(requestDisconnect, false)
	conn.controlConn.Close()

	return nil
}

func (conn *Conn) sendAuthentication(channelID ChannelID, streamKey []byte) (err error) {
	resp, err := conn.sendControlMessage(requestHmac, true)
	if err != nil {
		return err
	}
	split := strings.Split(resp, " ")

	hmacHexString := split[1]
	decoded, err := hex.DecodeString(hmacHexString)
	if err != nil {
		return err
	}

	hash := hmac.New(sha512.New, streamKey)
	hash.Write(decoded)

	hmacPayload := hash.Sum(nil)

	resp, err = conn.sendControlMessage(fmt.Sprintf(requestConnect, channelID, hex.EncodeToString(hmacPayload)), true)
	if err := checkFtlResponse(resp, err, responseOk); err != nil {
		return err
	}

	return nil
}

func (conn *Conn) sendMetadataBatch() error {
	// fake for now
	attrs := []string{
		// Generic
		fmt.Sprintf(metaProtocolVersion, VersionMajor, VersionMinor),
		fmt.Sprintf(metaVendorName, "rtmp-ingest"),
		fmt.Sprintf(metaVendorVersion, "1.0"),
		// Video
		fmt.Sprintf(metaVideo, "true"),
		fmt.Sprintf(metaVideoCodec, "H264"),
		fmt.Sprintf(metaVideoHeight, 720),
		fmt.Sprintf(metaVideoWidth, 1280),
		fmt.Sprintf(metaVideoPayloadType, 96),
		fmt.Sprintf(metaVideoIngestSSRC, conn.channelId+1),
		// Audio
		fmt.Sprintf(metaAudio, "true"),
		fmt.Sprintf(metaAudioCodec, "OPUS"), // This is a lie, its AAC
		fmt.Sprintf(metaAudioPayloadType, 97),
		fmt.Sprintf(metaAudioIngestSSRC, conn.channelId),
	}
	for _, v := range attrs {
		_, err := conn.sendControlMessage(v, false)
		if err != nil {
			return err
		}
	}

	return nil
}

func (conn *Conn) sendMediaStart() (err error) {
	resp, err := conn.sendControlMessage(requestDot, true)
	if err != nil {
		return err
	}

	matches := clientMediaPortRegex.FindAllStringSubmatch(resp, 1)
	if len(matches) < 1 {
		return err
	}
	conn.AssignedMediaPort, err = strconv.Atoi(matches[0][1])
	if err != nil {
		return err
	}

	return nil
}
func (conn *Conn) heartbeat() {
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				resp, err := conn.sendControlMessage(requestPing, true)
				if err := checkFtlResponse(resp, err, responsePong); err != nil {
					conn.failedHeartbeats += 1
					if conn.failedHeartbeats >= allowedHeartbeatFailures {
						conn.Close()
						return
					}
				} else {
					conn.failedHeartbeats = 0
				}
			case <-conn.quitTimer:
				ticker.Stop()
				return
			}
		}
	}()
}

func (conn *Conn) sendControlMessage(message string, needResponse bool) (resp string, err error) {
	err = conn.writeControlMessage(message)
	if err != nil {
		return "", err
	}

	if needResponse {
		return conn.readControlMessage()
	}

	return "", nil
}

func (conn *Conn) writeControlMessage(message string) error {
	final := message + "\r\n\r\n"
	log.Printf("SEND: %q", final)
	_, err := conn.controlConn.Write([]byte(final))
	return err
}

// readControlMessage forces a read, and closes the connection if it doesn't get what it wants
func (conn *Conn) readControlMessage() (string, error) {
	// Give the server 5 seconds to respond to our read request
	conn.controlConn.SetReadDeadline(time.Now().Add(time.Second * 5))

	recv, err := conn.controlScanner.ReadString('\n')
	if err != nil {
		return "", err
	}

	log.Printf("RECV: %q", recv)
	return strings.TrimRight(recv, "\n"), nil
}

func checkFtlResponse(resp string, err error, expected string) error {
	if err != nil {
		return err
	}
	if resp != expected {
		return errors.New("unexpected reply from server: " + resp)
	}
	return nil
}
