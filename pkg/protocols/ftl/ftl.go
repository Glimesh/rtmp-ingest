package ftl

import "regexp"

var (
	DefaultPort = 8084
	connectRegex   = regexp.MustCompile(`CONNECT ([0-9]+) \$([0-9a-f]+)`)
	clientHmacRegex = regexp.MustCompile(`200 ([0-9]+) \$([0-9a-f]+)`)
	clientMediaPortRegex = regexp.MustCompile(`200 hi\. Use UDP port (\d+)`)
	attributeRegex = regexp.MustCompile(`(.+): (.+)`)
)

const (
	hmacPayloadSize     = 128
	// Client Requests
	// All client requests must be prepended with \r\n, but we can do that in the
	// sendMessage function
	requestHmac = "HMAC"
	requestConnect = "CONNECT %d $%s"
	requestDot = "."
	requestPing = "PING"

	// Client Metadata
	metaProtocolVersion = "ProtocolVersion: %d.%d"
	metaVendorName = "VendorName: %s"
	metaVendorVersion = "VendorVersion: %s"
	metaVideo = "Video: %s"
	metaVideoCodec = "VideoCodec: %s"
	metaVideoHeight = "VideoHeight: %s"
	metaVideoWidth = "VideoWidth: %s"
	metaVideoPayloadType = "VideoPayloadType: %d"
	metaVideoIngestSSRC = "VideoIngestSSRC: %d"
	metaAudio = "Audio: %s"
	metaAudioCodec = "AudioCodec: %s"
	metaAudioPayloadType = "AudioPayloadType: %d"
	metaAudioIngestSSRC = "AudioIngestSSRC: %d"

	// Server Responses
	responseHmacPayload = "200 %s\n"
	responseOk          = "200\n"
	responsePong        = "201\n"
	responseMediaPort   = "200. Use UDP port %d\n"
)