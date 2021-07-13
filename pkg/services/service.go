package services

type Service interface {
	// Name of the service, eg: Glimesh
	Name() string
	// Connect to the service
	Connect() error
	// GetHmacKey Get the private HMAC key for a given channel ID
	GetHmacKey(channelID uint32) ([]byte, error)
	// StartStream Starts a stream for a given channel
	StartStream(channelID uint32) (uint32, error)
	// EndStream Marks the given stream ID as ended on the service
	EndStream(streamID uint32) error
	// UpdateStreamMetadata Updates the service with additional metadata about a stream
	UpdateStreamMetadata(streamID uint32, metadata StreamMetadata) error
	// SendJpegPreviewImage Sends a JPEG preview image of a stream to the service
	SendJpegPreviewImage(streamID uint32) error
}

// TODO: Move outta here
type StreamMetadata struct {
}
