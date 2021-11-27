package main

import (
	"errors"
	"fmt"
	"net"

	"github.com/clone1018/rtmp-ingest/pkg/orchestrator"
	"github.com/clone1018/rtmp-ingest/pkg/protocols/ftl"
	"github.com/clone1018/rtmp-ingest/pkg/services"
	"github.com/pion/rtp/v2"
)

type Stream struct {
	// authenticated is set after the stream has successfully authed with a remote service
	authenticated bool
	// mediaStarted is set after media bytes have come in from the client
	mediaStarted bool

	// rtpWriter is a multi writer for all of our relays
	rtpWriter *multiWriter
	relays    map[string]*StreamRelay

	channelID ftl.ChannelID
	streamID  ftl.StreamID
	streamKey []byte
}

type StreamRelay struct {
	targetHostname string
	ftlControlPort int
	ftlMediaPort   int

	ftlClient *ftl.Conn
	ftlMedia  net.Conn
}

type StreamManager struct {
	service      services.Service
	orchestrator orchestrator.Client
	streams      map[ftl.ChannelID]*Stream
}

func NewStreamManager(orchestrator orchestrator.Client, service services.Service) StreamManager {
	return StreamManager{
		orchestrator: orchestrator,
		service:      service,
		streams:      make(map[ftl.ChannelID]*Stream),
	}
}

func (mgr *StreamManager) NewStream(channelID ftl.ChannelID) error {
	mw := MultiWriter()
	// Helps the compiler know what we're talking about
	var m = mw.(*multiWriter)

	stream := &Stream{
		authenticated: false,
		mediaStarted:  false,
		channelID:     channelID,
		relays:        make(map[string]*StreamRelay),
		rtpWriter:     m,
	}

	if _, exists := mgr.streams[channelID]; exists {
		return errors.New("stream already exists in stream manager state")
	}
	mgr.streams[channelID] = stream

	return nil
}

func (mgr *StreamManager) Authenticate(channelID ftl.ChannelID, streamKey []byte) error {
	stream, err := mgr.GetStream(channelID)
	if err != nil {
		return err
	}

	actualKey, err := mgr.service.GetHmacKey(channelID)
	if err != nil {
		return err
	}
	if string(streamKey) != string(actualKey) {
		return errors.New("incorrect stream key")
	}

	stream.authenticated = true
	stream.streamKey = streamKey

	return nil
}

func (mgr *StreamManager) StartStream(channelID ftl.ChannelID) (*Stream, error) {
	stream, err := mgr.GetStream(channelID)
	if err != nil {
		return &Stream{}, err
	}

	streamID, err := mgr.service.StartStream(channelID)
	if err != nil {
		return &Stream{}, err
	}

	stream.streamID = streamID

	err = mgr.orchestrator.SendStreamPublishing(orchestrator.StreamPublishingMessage{
		Context:   1,
		ChannelID: stream.channelID,
		StreamID:  stream.streamID,
	})
	if err != nil {
		return &Stream{}, err
	}

	return stream, err
}

func (mgr *StreamManager) StopStream(channelID ftl.ChannelID) (err error) {
	stream, err := mgr.GetStream(channelID)
	if err != nil {
		return err
	}

	// Close all of our FTL relay connections
	for _, relay := range stream.relays {
		if err := relay.close(); err != nil {
			return err
		}
	}

	// Tell the orchestrator the stream has ended
	if err := mgr.orchestrator.SendStreamPublishing(orchestrator.StreamPublishingMessage{
		Context:   0,
		ChannelID: stream.channelID,
		StreamID:  stream.streamID,
	}); err != nil {
		return err
	}

	// Tell the service the stream has ended
	if err := mgr.service.EndStream(stream.streamID); err != nil {
		return err
	}

	return nil
}

func (mgr *StreamManager) RelayMedia(channelID ftl.ChannelID, targetHostname string, ftlPort int, streamKey []byte) error {
	stream, err := mgr.GetStream(channelID)
	if err != nil {
		return err
	}

	if _, exists := stream.relays[targetHostname]; exists {
		return errors.New("already sending media packets to this relay")
	}

	// Request to relay
	ftlAddr := fmt.Sprintf("%s:%d", targetHostname, ftlPort)
	ftlClient, err := ftl.Dial(ftlAddr, channelID, streamKey)
	if err != nil {
		return err
	}

	mediaAddr := fmt.Sprintf("%s:%d", targetHostname, ftlClient.AssignedMediaPort)
	ftlMedia, err := net.Dial("udp", mediaAddr)
	if err != nil {
		return err
	}

	// Read nothing
	//go func() {
	//	buffer := make([]byte, 1024)
	//	_, err = ftlMedia.Read(buffer)
	//	if err != nil {
	//		panic(err)
	//	}
	//}()

	// setting the rtpWriter is enough to get a stream of packets coming through
	stream.relays[targetHostname] = &StreamRelay{
		targetHostname: targetHostname,
		ftlControlPort: ftlPort,
		ftlMediaPort:   ftlClient.AssignedMediaPort,
		ftlClient:      ftlClient,
		ftlMedia:       ftlMedia,
	}

	// Add to MultiWriter
	stream.rtpWriter.Append(ftlMedia)

	return nil
}

func (mgr *StreamManager) StopRelay(channelID ftl.ChannelID, targetHostname string) error {
	stream, err := mgr.GetStream(channelID)
	if err != nil {
		return err
	}

	// Relay might not actually exist in our state
	if _, exists := stream.relays[targetHostname]; !exists {
		return errors.New("relay does not exist in state")
	}

	// Remove from MultiWriter
	stream.rtpWriter.Remove(stream.relays[targetHostname].ftlMedia)

	if err := stream.relays[targetHostname].close(); err != nil {
		return err
	}

	delete(stream.relays, targetHostname)
	return nil
}

func (stream *Stream) WriteRTP(packet *rtp.Packet) error {
	buf, err := packet.Marshal()
	if err != nil {
		return err
	}
	_, err = stream.rtpWriter.ConcurrentWrite(buf)
	return err
}

// AddStream adds a stream to the manager in an unauthenticated state
//func (mgr *StreamManager) AddStream(connHandler *ConnHandler) error {
//	if _, exists := mgr.streams[connHandler.channelID]; exists {
//		return errors.New("stream already exists in state")
//	}
//	mgr.streams[connHandler.channelID] = connHandler
//	return nil
//}

func (mgr *StreamManager) RemoveStream(id ftl.ChannelID) error {
	if _, exists := mgr.streams[id]; !exists {
		return errors.New("stream does not exist in state")
	}
	delete(mgr.streams, id)
	return nil
}
func (mgr *StreamManager) GetStream(id ftl.ChannelID) (*Stream, error) {
	if _, exists := mgr.streams[id]; !exists {
		return &Stream{}, errors.New("stream does not exist in state")
	}
	return mgr.streams[id], nil
}

func (relay *StreamRelay) close() error {
	if err := relay.ftlMedia.Close(); err != nil {
		return err
	}
	if err := relay.ftlClient.Close(); err != nil {
		return err
	}

	return nil
}
