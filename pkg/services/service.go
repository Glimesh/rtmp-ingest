package services

import "github.com/Glimesh/rtmp-ingest/pkg/protocols/ftl"

type Service interface {
	// Name of the service, eg: Glimesh
	Name() string
	// Connect to the service
	Connect() error
	// GetHmacKey Get the private HMAC key for a given channel ID
	GetHmacKey(channelID ftl.ChannelID) ([]byte, error)
	// StartStream Starts a stream for a given channel
	StartStream(channelID ftl.ChannelID) (ftl.StreamID, error)
	// EndStream Marks the given stream ID as ended on the service
	EndStream(streamID ftl.StreamID) error
	// UpdateStreamMetadata Updates the service with additional metadata about a stream
	UpdateStreamMetadata(streamID ftl.StreamID, metadata StreamMetadata) error
	// SendJpegPreviewImage Sends a JPEG preview image of a stream to the service
	SendJpegPreviewImage(streamID ftl.StreamID) error
}

// TODO: Move outta here
type StreamMetadata struct {
}
