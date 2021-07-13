package orchestrator

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"log"
	"net"
)

type Connection struct {
	Connected     bool
	transport     net.Conn
	lastMessageID uint8

	OnChannelSubscription func(message ChannelSubscriptionMessage)
	OnStreamRelaying func(message StreamRelayingMessage)
}

func (conn *Connection) Connect(address string, regionCode string, hostname string) error {
	transport, err := net.Dial("tcp", address)
	if err != nil {
		return err
	}

	conn.transport = transport
	conn.Connected = true
	conn.lastMessageID = 1

	conn.SendIntro(IntroMessage{
		VersionMajor:    0,
		VersionMinor:    0,
		VersionRevision: 0,
		RelayLayer:      0,
		RegionCode:      regionCode,
		Hostname:        hostname,
	})

	go conn.eternalRead()
	return nil
}

func (conn *Connection) eternalRead() {
	// I think this has to do something funky
	// https://github.com/Glimesh/janus-ftl-orchestrator/blob/974b55956094d1e1d29e060c8fb056d522a3d153/inc/FtlConnection.h

	scanner := bufio.NewScanner(conn.transport)
	scanner.Split(splitOrchestratorMessages)
	for scanner.Scan() {
		log.Println("Orchestrator Scan")

		// ready to go header + message
		buf := scanner.Bytes()

		//log.Printf("Receiving Orchestrator: %v\n", buf)
		log.Printf("Receiving Orchestrator Hex: %s\n", insertNth(hex.EncodeToString(buf), 2))

		conn.parseMessage(buf)
	}
	if err := scanner.Err(); err != nil {
		log.Println("Error decoding Orchestrator input:", err)
	}
}

func splitOrchestratorMessages(data []byte, atEOF bool) (advance int, token []byte, err error) {
	// Return nothing if at end of file and no data passed
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	if len(data) >= 4 {
		//log.Printf("Receiving Raw Hex: %s\n", insertNth(hex.EncodeToString(data), 2))
		messageHeader := DecodeMessageHeader(data)
		messageSize := int(messageHeader.PayloadLength) + 4
		return messageSize, data[0:messageSize], nil
	}

	// If at end of file with data return the data
	if atEOF {
		return len(data), data, nil
	}

	return
}


func (conn *Connection) Close() {
	conn.SendOutro(OutroMessage{Reason: "Going away"})

	if err := conn.transport.Close(); err != nil {
		panic(err)
	}
	conn.Connected = false
}

func (conn *Connection) parseMessage(raw []byte)  {
	messageHeader := DecodeMessageHeader(raw)
	message := raw[4:4+int(messageHeader.PayloadLength)]

	conn.handleMessage(*messageHeader, message)
}

func (conn *Connection) handleMessage(header MessageHeader, payload []byte) {
	log.Printf("parseMessage: %+v\n", header)

	if !header.Request {
		// We don't need to bother decoding
		// Responses are meaningless
		return
	}

	switch header.Type {
	case TypeIntro:
		log.Println("RECV TypeIntro")
		//something := DecodeIntroMessage(payload)
		//fmt.Printf("Decoded: %v\n", something)
	case TypeOutro:
		log.Println("RECV TypeOutro")
	case TypeNodeState:
		log.Println("RECV TypeNodeState")
	case TypeChannelSubscription:
		log.Println("RECV TypeChannelSubscription")
		//if conn.OnChannelSubscription != nil {
			// conn.OnChannelSubscription
		//}
	case TypeStreamPublishing:
		log.Println("RECV TypeStreamPublishing")
		//something := DecodeStreamPublishingMessage(payload)
		//fmt.Printf("Decoded: %v\n", something)
	case TypeStreamRelaying:
		log.Println("RECV TypeStreamRelaying")
		if conn.OnStreamRelaying != nil {
			conn.OnStreamRelaying(*DecodeStreamRelayingMessage(payload))
		}
	}
}

func (conn *Connection) SendMessage(messageType uint8, payload []byte) error {
	if !conn.Connected {
		return errors.New("orchestrator connection is closed")
	}

	// Construct the message
	message := MessageHeader{
		Request:       true,
		Success:       true,
		Type:          messageType,
		ID:            conn.lastMessageID,
		PayloadLength: uint16(len(payload)),
	}
	messageBuffer := message.Encode()
	messageBuffer = append(messageBuffer, payload...)

	log.Printf("Sending Orchestrator: %v\n", messageBuffer)
	log.Printf("Sending Orchestrator Hex: %s\n", insertNth(hex.EncodeToString(messageBuffer), 2))
	_, err := conn.transport.Write(messageBuffer)
	if err != nil {
		panic(err)
		return err
	}

	conn.lastMessageID = conn.lastMessageID + 1

	return nil
}

func insertNth(s string, n int) string {
	var buffer bytes.Buffer
	n1 := n - 1
	l1 := len(s) - 1
	for i, r := range s {
		buffer.WriteRune(r)
		if i%n == n1 && i != l1 {
			buffer.WriteRune(' ')
		}
	}
	return buffer.String()
}

func (conn Connection) SendIntro(message IntroMessage) {
	conn.SendMessage(TypeIntro, message.Encode())
}
func (conn Connection) SendOutro(message OutroMessage) {
	conn.SendMessage(TypeOutro, message.Encode())
}
func (conn Connection) SendNodeState(message NodeStateMessage) {
	conn.SendMessage(TypeNodeState, message.Encode())
}
func (conn Connection) SendChannelSubscription(message ChannelSubscriptionMessage) {
	conn.SendMessage(TypeChannelSubscription, message.Encode())
}
func (conn Connection) SendStreamPublishing(message StreamPublishingMessage) {
	conn.SendMessage(TypeStreamPublishing, message.Encode())
}
func (conn Connection) SendStreamRelaying(message StreamRelayingMessage) {
	conn.SendMessage(TypeStreamRelaying, message.Encode())
}
