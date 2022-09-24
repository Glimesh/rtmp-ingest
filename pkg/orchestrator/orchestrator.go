package orchestrator

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"errors"
	"net"

	"github.com/sirupsen/logrus"
)

type Client struct {
	ClientHostname string

	config    *Config
	logger    logrus.FieldLogger
	callbacks Callbacks

	connected     bool
	transport     net.Conn
	lastMessageID uint8
}

type Callbacks struct {
	OnIntro               func(header MessageHeader, message IntroMessage)
	OnOutro               func(header MessageHeader, message OutroMessage)
	OnNodeState           func(header MessageHeader, message NodeStateMessage)
	OnChannelSubscription func(header MessageHeader, message ChannelSubscriptionMessage)
	OnStreamPublishing    func(header MessageHeader, message StreamPublishingMessage)
	OnStreamRelaying      func(header MessageHeader, message StreamRelayingMessage)
}

type Config struct {
	// Address of the remote orchestrator ip:port format
	Address string
	// RegionCode we are representing
	RegionCode string
	// Hostname for ourselves, so edges know how to reach us
	Hostname string
	// Logger for orchestrator client messages
	Logger logrus.FieldLogger
	// Handler for callbacks
	Callbacks Callbacks
}

func NewClient(config Config) Client {
	return Client{
		ClientHostname: config.Hostname,
		config:         &config,
	}
}

func (client *Client) Connect(transport net.Conn) error {
	client.transport = transport
	client.lastMessageID = 1
	client.connected = true

	if client.config.Logger != nil {
		client.logger = client.config.Logger
	} else {
		client.logger = logrus.New()
	}

	client.callbacks = client.config.Callbacks

	err := client.SendIntro(IntroMessage{
		VersionMajor:    0,
		VersionMinor:    0,
		VersionRevision: 0,
		RelayLayer:      0,
		RegionCode:      client.config.RegionCode,
		Hostname:        client.config.Hostname,
	})
	if err != nil {
		client.connected = false
		return err
	}

	go client.eternalRead()
	return nil
}

func (client *Client) eternalRead() {
	// I think this has to do something funky
	// https://github.com/Glimesh/janus-ftl-orchestrator/blob/974b55956094d1e1d29e060c8fb056d522a3d153/inc/FtlConnection.h

	scanner := bufio.NewScanner(client.transport)
	scanner.Split(splitOrchestratorMessages)
	for scanner.Scan() {
		if !client.connected {
			return
		}

		// ready to go header + message
		buf := scanner.Bytes()

		client.logger.Debugf("Receiving Orchestrator Hex: %s", insertNth(hex.EncodeToString(buf), 2))

		client.parseMessage(buf)
	}
	if err := scanner.Err(); err != nil {
		// For now if we lose the connection with the orchestrator we crash the server
		client.logger.Fatal("Error decoding Orchestrator input:", err)
	}
}

func splitOrchestratorMessages(data []byte, atEOF bool) (advance int, token []byte, err error) {
	// Return nothing if at end of file and no data passed
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}

	if len(data) >= 4 {
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

func (client *Client) Close() error {
	client.logger.Info("Sending Outro Message to Orchestrator")
	if !client.connected {
		// Already closed
		return nil
	}

	// Both of these can error, but we're trying to close the connection anyway
	client.SendOutro(OutroMessage{Reason: "Going away"})
	// client.transport.Close()

	client.connected = false
	return nil
}

func (client *Client) parseMessage(raw []byte) {
	messageHeader := DecodeMessageHeader(raw)
	message := raw[4 : 4+int(messageHeader.PayloadLength)]

	client.handleMessage(*messageHeader, message)
}

func (client *Client) handleMessage(header MessageHeader, payload []byte) {
	if !header.Request {
		// We don't need to bother decoding
		// Responses are meaningless, unless maybe we need them as a confirmation?
		return
	}

	client.logger.Debugf("Got message from Orchestrator: %#v", header)
	switch header.Type {
	case TypeIntro:
		if client.callbacks.OnIntro != nil {
			client.callbacks.OnIntro(header, DecodeIntroMessage(payload))
		}
	case TypeOutro:
		if client.callbacks.OnOutro != nil {
			client.callbacks.OnOutro(header, DecodeOutroMessage(payload))
		}
	case TypeNodeState:
		if client.callbacks.OnNodeState != nil {
			client.callbacks.OnNodeState(header, DecodeNodeStateMessage(payload))
		}
	case TypeChannelSubscription:
		if client.callbacks.OnChannelSubscription != nil {
			client.callbacks.OnChannelSubscription(header, DecodeChannelSubscriptionMessage(payload))
		}
	case TypeStreamPublishing:
		if client.callbacks.OnStreamPublishing != nil {
			client.callbacks.OnStreamPublishing(header, DecodeStreamPublishingMessage(payload))
		}
	case TypeStreamRelaying:
		if client.callbacks.OnStreamRelaying != nil {
			client.callbacks.OnStreamRelaying(header, DecodeStreamRelayingMessage(payload))
		}
	}
}

func (client *Client) SendMessage(messageType uint8, payload []byte) error {
	if !client.connected {
		return errors.New("orchestrator connection is closed")
	}

	// Construct the message
	message := MessageHeader{
		Request:       true,
		Success:       true,
		Type:          messageType,
		ID:            client.lastMessageID,
		PayloadLength: uint16(len(payload)),
	}
	messageBuffer := message.Encode()
	messageBuffer = append(messageBuffer, payload...)

	client.logger.Debugf("Sending Orchestrator: %#v", messageBuffer)
	_, err := client.transport.Write(messageBuffer)
	if err != nil {
		return err
	}

	client.lastMessageID += 1

	return nil
}

func (client *Client) SendResponseMessage(messageType uint8, messageID uint8, payload []byte) error {
	if !client.connected {
		return errors.New("orchestrator connection is closed")
	}

	// Construct the message
	message := MessageHeader{
		Request:       false,
		Success:       true,
		Type:          messageType,
		ID:            messageID,
		PayloadLength: uint16(len(payload)),
	}
	messageBuffer := message.Encode()
	messageBuffer = append(messageBuffer, payload...)

	client.logger.Debugf("Sending Orchestrator: %#v with %#v", message, payload)
	_, err := client.transport.Write(messageBuffer)
	if err != nil {
		return err
	}

	client.lastMessageID += 1

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

func (client Client) SendIntro(message IntroMessage) error {
	return client.SendMessage(TypeIntro, message.Encode())
}
func (client Client) SendOutro(message OutroMessage) error {
	return client.SendMessage(TypeOutro, message.Encode())
}
func (client Client) SendNodeState(message NodeStateMessage) error {
	return client.SendMessage(TypeNodeState, message.Encode())
}
func (client Client) SendChannelSubscription(message ChannelSubscriptionMessage) error {
	return client.SendMessage(TypeChannelSubscription, message.Encode())
}
func (client Client) SendStreamPublishing(message StreamPublishingMessage) error {
	return client.SendMessage(TypeStreamPublishing, message.Encode())
}
func (client Client) SendStreamRelaying(message StreamRelayingMessage) error {
	return client.SendMessage(TypeStreamRelaying, message.Encode())
}
