package main

import (
	"errors"
	"fmt"
	"net"

	"github.com/Glimesh/rtmp-ingest/pkg/orchestrator"
	"github.com/Glimesh/rtmp-ingest/pkg/protocols/ftl"
	"github.com/Glimesh/rtmp-ingest/pkg/services"
	"github.com/pion/rtp/v2"
)

type Stream struct {
	// authenticated is set after the stream has successfully authed with a remote service
	authenticated bool
	// mediaStarted is set after media bytes have come in from the client
	mediaStarted bool

	relays map[string]*StreamRelay
	// edgeWriter *edgeWriter

	channelID ftl.ChannelID
	streamID  ftl.StreamID
	streamKey []byte
}

type StreamRelay struct {
	targetHostname string
	ftlControlPort int
	ftlMediaPort   int

	ftlClient    *ftl.Conn
	ftlMedia     *net.UDPConn
	ftlMediaAddr *net.UDPAddr
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
	// edgeWriter := NewEdgeWriter()

	stream := &Stream{
		authenticated: false,
		mediaStarted:  false,
		channelID:     channelID,
		relays:        make(map[string]*StreamRelay),
		// edgeWriter:    edgeWriter,
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

	fmt.Printf("stream.relays: %#v\n", stream.relays)

	// Close all of our FTL relay connections
	for name, relay := range stream.relays {
		fmt.Printf("Closing %s\n", name)
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

	// We should likely just write the last keyframe packet we have
	// in order to get the relay started
	// Actual this needs to be sizable chunk of RTP so ignore for now

	// Request to relay

	// func(mediaConn net.Conn) error {
	// 	if _, exists := stream.relays[targetHostname]; !exists {
	// 		return nil
	// 	}

	// 	stream.rtpWriter.Remove(mediaConn)
	// 	fmt.Println("Removing RTP writer")

	// 	return nil
	// }
	ftlClient, err := ftl.Dial(targetHostname, ftlPort, channelID, streamKey)
	if err != nil {
		return err
	}

	stream.relays[targetHostname] = &StreamRelay{
		targetHostname: targetHostname,
		ftlControlPort: ftlPort,
		ftlMediaPort:   ftlClient.AssignedMediaPort,
		ftlClient:      ftlClient,
		ftlMedia:       ftlClient.MediaConn,
		ftlMediaAddr:   ftlClient.MediaAddr,
	}

	// setting the rtpWriter is enough to get a stream of packets coming through
	// ftlClient.MediaConn.SetDeadline(time.Now().Add(time.Second * 5))
	// stream.rtpWriter.Append(ftlClient.MediaConn)
	// stream.edgeWriter.new(targetHostname, ftlClient.MediaConn)

	// Heartbeat (blocking thread until we get disconnected)
	return ftlClient.Heartbeat()
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
	// stream.edgeWriter.remove(targetHostname)

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

	// This can error if the relay is removed in another thread, which is common because an edge will stop being a relay for a stream when there are no viewers on it.
	// TODO: Figure out how to conditionally error from this.
	// stream.edgeWriter.write(buf)
	for _, relay := range stream.relays {
		relay.ftlMedia.Write(buf)
	}

	return nil
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
		return errors.New("RemoveStream stream does not exist in state")
	}
	delete(mgr.streams, id)
	return nil
}
func (mgr *StreamManager) GetStream(id ftl.ChannelID) (*Stream, error) {
	if _, exists := mgr.streams[id]; !exists {
		return &Stream{}, errors.New("GetStream stream does not exist in state")
	}
	return mgr.streams[id], nil
}

func (relay *StreamRelay) close() error {
	if err := relay.ftlClient.Close(); err != nil {
		return err
	}

	return nil
}
