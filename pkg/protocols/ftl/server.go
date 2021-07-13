package ftl

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/pion/webrtc/v3"
	"log"
	"math/rand"
	"net"
	"strconv"
	"strings"
)

type Manager struct {
	Clients map[int]*FtlConnection
}

func NewManager() *Manager {
	return &Manager{
		Clients: make(map[int]*FtlConnection),
	}
}

type Callbacks struct {
	OnStreamStart func(channelID int, streamID int)
}

func (manager *Manager) Listen(callbacks Callbacks) {
	ftlControlConn, err := net.Listen("tcp", ":8084")
	if err != nil {
		// handle error
		panic(err)
	}
	fmt.Println("FTL listening on 8084")

	for {
		// Each client
		socket, ftlErr := ftlControlConn.Accept()

		ftlConn := FtlConnection{
			transport: socket,
			manager:   manager,
			Connected: true,
			Metadata:  &FtlConnectionMetadata{},
			Callbacks: callbacks,
		}

		if ftlErr != nil {
			panic(ftlErr)
		}

		go ftlConn.eternalRead()
	}
}

func (manager *Manager) AddChannel(channelId int, ftlConn *FtlConnection) {
	manager.Clients[channelId] = ftlConn
}

func (manager *Manager) WatchChannel(channelId int) (*webrtc.TrackLocalStaticRTP, *webrtc.TrackLocalStaticRTP, error) {
	if val, ok := manager.Clients[channelId]; ok {
		fmt.Printf("WatchChannel: %+v\n", val.mediaConn.videoTrack)
		return val.mediaConn.videoTrack, val.mediaConn.audioTrack, nil
	}

	return nil, nil, errors.New("could not find channel")
}

type FtlConnection struct {
	transport net.Conn
	manager   *Manager
	Connected bool
	Callbacks Callbacks

	// Not sure what this is used for?
	targetHostname string
	clientIP       string

	// Unique Channel ID
	channelID         int
	streamKey         string
	assignedMediaPort int

	// Pre-calculated hash we expect the client to return
	hmacPayload []byte
	// Hash the client has actually returned
	clientHmacHash []byte

	hasAuthenticated bool
	hmacRequested    bool

	Metadata  *FtlConnectionMetadata
	mediaConn *MediaConnection
}

type FtlConnectionMetadata struct {
	ProtocolVersion string
	VendorName      string
	VendorVersion   string

	HasVideo         bool
	VideoCodec       string
	VideoHeight      uint
	VideoWidth       uint
	VideoPayloadType uint
	VideoIngestSsrc  uint

	HasAudio         bool
	AudioCodec       string
	AudioPayloadType uint
	AudioIngestSsrc  uint
}

func (conn *FtlConnection) eternalRead() {
	// A previous read could have disconnected us already
	if !conn.Connected {
		return
	}

	scanner := bufio.NewScanner(conn.transport)
	scanner.Split(scanCRLF)

	for scanner.Scan() {
		payload := scanner.Text()

		// I'm not processing something correctly here
		if payload == "" {
			continue
		}

		conn.ProcessCommand(payload)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("Invalid input: %s", err)
	}

	conn.eternalRead()
}

func (conn *FtlConnection) SendMessage(message string) error {
	log.Printf("SEND: %q", message)
	_, err := conn.transport.Write([]byte(message))
	return err
}

func (conn *FtlConnection) Disconnect() error {
	conn.Connected = false
	return conn.transport.Close()
}

func (conn *FtlConnection) ProcessCommand(command string) {
	log.Printf("RECV: %q", command)
	if command == "HMAC" {
		conn.processHmacCommand()
	} else if strings.Contains(command, "DISCONNECT") {
		conn.processDisconnectCommand(command)
	} else if strings.Contains(command, "CONNECT") {
		conn.processConnectCommand(command)
	} else if strings.Contains(command, "PING") {
		conn.processPingCommand()
	} else if attributeRegex.MatchString(command) {
		conn.processAttributeCommand(command)
	} else if command == "." {
		conn.processDotCommand()
	} else {
		log.Printf("Unknown ingest command: %s", command)
	}
}

func (conn *FtlConnection) processHmacCommand() {
	hash := hmac.New(sha512.New, []byte("aBcDeFgHiJkLmNoPqRsTuVwXyZ123456"))

	block := make([]byte, hmacPayloadSize)
	rand.Read(block)
	hash.Write(block)

	encodedPayload := hex.EncodeToString(block)
	conn.hmacPayload = hash.Sum(nil)
	conn.SendMessage(fmt.Sprintf(responseHmacPayload, encodedPayload))
}

func (conn *FtlConnection) processDisconnectCommand(message string) {
	log.Println("Got Disconnect command, closing stuff.")
	if err := conn.mediaConn.writer.Close(); err != nil {
		panic(err)
	}
}

func (conn *FtlConnection) processConnectCommand(message string) {
	if conn.hmacRequested {
		log.Println("Control connection attempted multiple CONNECT handshakes")
		conn.Disconnect()
		return
	}

	matches := connectRegex.FindAllStringSubmatch(message, 3)
	if len(matches) < 1 {
		conn.Disconnect()
		return
	}
	args := matches[0]
	if len(args) < 3 {
		// malformed connection string
		conn.Disconnect()
		return
	}

	channelIdStr := args[1]
	hmacHashStr := args[2]

	channelId, err := strconv.Atoi(channelIdStr)
	if err != nil {
		log.Printf("Client provided invalid HMAC hash for channel %s, disconnecting...", channelIdStr)
		conn.Disconnect()
		return
	}

	hmacBytes, err := hex.DecodeString(hmacHashStr)
	if err != nil {
		log.Printf("Client provided HMAC hash that could not be hex decoded")
		conn.Disconnect()
		return
	}

	conn.hasAuthenticated = true
	conn.hmacRequested = true
	conn.channelID = channelId
	conn.clientHmacHash = hmacBytes

	if !hmac.Equal(conn.clientHmacHash, conn.hmacPayload) {
		log.Printf("Client provided invalid HMAC hash for channel %d, disconnecting...", channelId)
		conn.Disconnect()
		return
	}

	conn.SendMessage(responseOk)
}

func (conn *FtlConnection) processAttributeCommand(message string) {
	if !conn.hasAuthenticated {
		log.Println("Control connection attempted attribute command before authentication")
		conn.Disconnect()
		return
	}

	matches := attributeRegex.FindAllStringSubmatch(message, 3)
	if len(matches) < 1 || len(matches[0]) < 3 {
		conn.Disconnect()
		return
	}
	key, value := matches[0][1], matches[0][2]

	switch key {
	case "ProtocolVersion":
		conn.Metadata.ProtocolVersion = value
	case "VendorName":
		conn.Metadata.VendorName = value
	case "VendorVersion":
		conn.Metadata.VendorVersion = value
	// Video
	case "Video":
		conn.Metadata.HasVideo = parseAttributeToBool(value)
	case "VideoCodec":
		conn.Metadata.VideoCodec = value
	case "VideoHeight":
		conn.Metadata.VideoHeight = parseAttributeToUint(value)
	case "VideoWidth":
		conn.Metadata.VideoWidth = parseAttributeToUint(value)
	case "VideoPayloadType":
		conn.Metadata.VideoPayloadType = parseAttributeToUint(value)
	case "VideoIngestSSRC":
		conn.Metadata.VideoIngestSsrc = parseAttributeToUint(value)
	// Audio
	case "Audio":
		conn.Metadata.HasAudio = parseAttributeToBool(value)
	case "AudioCodec":
		conn.Metadata.AudioCodec = value
	case "AudioPayloadType":
		conn.Metadata.AudioPayloadType = parseAttributeToUint(value)
	case "AudioIngestSSRC":
		conn.Metadata.AudioIngestSsrc = parseAttributeToUint(value)
	default:
		log.Printf("Unexpected Attribute: %q\n", message)
	}
}

func parseAttributeToUint(input string) uint {
	u, _ := strconv.ParseUint(input, 10, 32)
	return uint(u)
}
func parseAttributeToBool(input string) bool {
	return input == "true"
}

func (conn *FtlConnection) processDotCommand() {
	if !conn.hasAuthenticated {
		log.Println("Client attempted to start stream without valid authentication.")
		conn.Disconnect()
		return
	}

	// Get random available port
	// Start media server and assign to FtlConnection
	mediaConn := &MediaConnection{
		Connected: false,
		ChannelId: conn.channelID,
		StreamId:  conn.channelID,
		// TODO: Do this conversion during attribute collection so we don't miss any data
		VideoPayloadType: uint8(conn.Metadata.VideoPayloadType),
		AudioPayloadType: uint8(conn.Metadata.AudioPayloadType),
		VideoIngestSsrc: conn.Metadata.VideoIngestSsrc,
		AudioIngestSsrc: conn.Metadata.AudioIngestSsrc,
	}

	err := mediaConn.Listen()
	if err != nil {
		conn.Disconnect()
		log.Println("Could not start media connection: ", err)
		return
	}

	conn.mediaConn = mediaConn
	conn.assignedMediaPort = mediaConn.port

	// Push it to a clients map so we can reference it later
	conn.manager.AddChannel(conn.channelID, conn)

	if conn.Callbacks.OnStreamStart != nil {
		conn.Callbacks.OnStreamStart(conn.channelID, 1234)
	}

	conn.SendMessage(fmt.Sprintf(responseMediaPort, conn.assignedMediaPort))
}

func (conn *FtlConnection) processPingCommand() {
	conn.SendMessage(responsePong)
}

func dropCR(data []byte) []byte {
	if len(data) > 0 && data[len(data)-1] == '\r' {
		return data[0 : len(data)-1]
	}
	return data
}

func scanCRLF(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.Index(data, []byte{'\r', '\n'}); i >= 0 {
		// We have a full newline-terminated line.
		return i + 2, dropCR(data[0:i]), nil
	}
	// If we're at EOF, we have a final, non-terminated line. Return it.
	if atEOF {
		return len(data), dropCR(data), nil
	}
	// Request more data.
	return 0, nil, nil
}
