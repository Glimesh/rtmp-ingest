package ftl

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"net"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media/h264writer"
)

// Browser - 50.503
// OBS     - 50.332 - 171
// Viewer  - 50.118 - 214

const (
	// This packet size is ideal for super fast video, but the theory is there's
	// going to be numerous problems with networking equipment not accepting the
	// packet size, especially for UDP.
	//packetMtu = 2048 // 67 ms -- 32 frames on 240 video -- 213ms on clock
	// I'm guessing for these two, the packet differences are not great enough to
	// overcome the 30/60 fps most people stream at. So you see no difference.
	//packetMtu = 1600 // 100 ms -- 30 frames on 240 video -- UDP MTU allegedly
	//packetMtu = 1500 // 100 ms gtg latency - 144ms on clock
	//packetMtu = 1460 // UDP MTU
	packetMtu = 1392
	// FTL-SDK recommends 1392 MTU
)

type MediaConnection struct {
	Connected bool
	ChannelId int
	StreamId  int

	// RTP Packet Payload Type
	VideoPayloadType uint8
	VideoIngestSsrc  uint
	// RTP Packet Payload Type
	AudioPayloadType uint8
	AudioIngestSsrc  uint

	transport *net.UDPConn
	port      int

	videoTrack *webrtc.TrackLocalStaticRTP
	audioTrack *webrtc.TrackLocalStaticRTP
	writer     *h264writer.H264Writer

	readVideoBytes int
	readAudioBytes int
}

func (conn *MediaConnection) Listen() error {
	udpAddr, err := net.ResolveUDPAddr("udp", ":0")
	if err != nil {
		return err
	}
	mediaConn, mediaErr := net.ListenUDP("udp", udpAddr)
	if mediaErr != nil {
		return err
	}

	conn.port = mediaConn.LocalAddr().(*net.UDPAddr).Port
	conn.transport = mediaConn
	conn.Connected = true

	err = conn.createMediaTracks()
	if err != nil {
		conn.Close()
		return err
	}

	log.Printf("Listening for UDP connections on: %d", conn.port)

	writer, err := h264writer.New("derp.mp4")
	if err != nil {
		panic(err)
	}
	conn.writer = writer

	go conn.eternalRead()
	log.Printf("Got past the eternal read.")

	return nil
}

func (conn *MediaConnection) Close() error {
	return conn.transport.Close()
}

func (conn *MediaConnection) createMediaTracks() error {
	var err error
	// Create a video track
	conn.videoTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: "video/h264"}, "video", "pion")
	if err != nil {
		return err
	}
	conn.readVideoBytes = 0

	// Create an audio track
	conn.audioTrack, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: "audio/opus"}, "audio", "pion")
	if err != nil {
		return err
	}
	conn.readAudioBytes = 0

	return nil
}

func (conn *MediaConnection) eternalRead() {
	if !conn.Connected {
		return
	}

	inboundRTPPacket := make([]byte, packetMtu)

	n, _, err := conn.transport.ReadFrom(inboundRTPPacket)
	if err != nil {
		fmt.Printf("error during read: %s", err)
		conn.Close()
		return
	}
	packet := &rtp.Packet{}
	if err = packet.Unmarshal(inboundRTPPacket[:n]); err != nil {
		fmt.Printf("Error unmarshaling RTP packet %s\n", err)
	}

	// The FTL client actually tells us what PayloadType to use for these: VideoPayloadType & AudioPayloadType
	if packet.Header.PayloadType == conn.VideoPayloadType {
		log.Printf("Keyframe: %v\n", isKeyFrame(packet.Payload))
		if err := conn.writer.WriteRTP(packet); err != nil {
			panic(err)
		}

		if err := conn.videoTrack.WriteRTP(packet); err != nil {
			fmt.Println("Error writing RTP to the video track: ", err)
			conn.Close()
			return
		}
		conn.readVideoBytes = conn.readVideoBytes + n
	} else if packet.Header.PayloadType == conn.AudioPayloadType {
		if err := conn.audioTrack.WriteRTP(packet); err != nil {
			fmt.Println("Error writing RTP to the audio track: ", err)
			conn.Close()
			return
		}
		conn.readAudioBytes = conn.readAudioBytes + n
	}

	conn.eternalRead()
}

//https://github.com/Glimesh/janus-ftl-plugin/blob/b7c650ca898d0ca1635d9481b7df7aca087eef64/src/FtlMediaConnection.cpp#L366
func isKeyFrame(data []byte) bool {
	const typeSTAPA = 24

	var word uint32

	payload := bytes.NewReader(data)
	err := binary.Read(payload, binary.BigEndian, &word)

	if err != nil || (word&0x1F000000)>>24 != typeSTAPA {
		return false
	}

	return word&0x1F == 7
}
