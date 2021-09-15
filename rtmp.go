package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/clone1018/rtmp-ingest/pkg/protocols/ftl"
	"github.com/kentuckyfriedtakahe/go-fdkaac/fdkaac"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/sirupsen/logrus"
	"github.com/yutopp/go-flv/tag"
	flvtag "github.com/yutopp/go-flv/tag"
	"github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"
	opus "gopkg.in/hraban/opus.v2"
)

func NewRTMPServer(streamManager StreamManager, log logrus.FieldLogger) {
	log.Info("Starting RTMP Server on :1935")

	tcpAddr, err := net.ResolveTCPAddr("tcp", ":1935")
	if err != nil {
		log.Fatal(err)
	}

	listener, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		log.Fatal(err)
	}

	srv := rtmp.NewServer(&rtmp.ServerConfig{
		OnConnect: func(conn net.Conn) (io.ReadWriteCloser, *rtmp.ConnConfig) {
			return conn, &rtmp.ConnConfig{
				Handler: &ConnHandler{
					manager:   streamManager,
					log:       log,
					quitTimer: make(chan bool, 1),
				},

				ControlState: rtmp.StreamControlStateConfig{
					DefaultBandwidthWindowSize: 6 * 1024 * 1024 / 8,
				},
				//Logger: log.WithField("app", "yutopp/go-rtmp"),
			}
		},
	})
	if err := srv.Serve(listener); err != nil {
		log.Fatal(err)
	}
}

type ConnHandler struct {
	rtmp.DefaultHandler
	manager StreamManager

	log logrus.FieldLogger

	channelID     ftl.ChannelID
	streamID      ftl.StreamID
	streamKey     []byte
	authenticated bool

	stream *Stream

	sequencer       rtp.Sequencer
	videoPacketizer rtp.Packetizer
	audioPacketizer rtp.Packetizer
	videoClockRate  uint32
	audioClockRate  uint32

	audioPackets     int
	videoPackets     int
	lastVideoPackets int

	lastKeyFrames   int
	lastInterFrames int

	sps []byte
	pps []byte

	quitTimer    chan bool
	audioDecoder *fdkaac.AacDecoder
	audioBuffer  []byte
	audioEncoder *opus.Encoder

	audioTimestamp uint32
}

func (h *ConnHandler) OnServe(conn *rtmp.Conn) {
	h.log.Info("OnServe: %#v", conn)
}

func (h *ConnHandler) OnConnect(timestamp uint32, cmd *rtmpmsg.NetConnectionConnect) (err error) {
	h.log.Info("OnConnect: %#v", cmd)

	h.videoClockRate = 90000
	h.audioClockRate = 48000

	// h.ctx = avformat.AvformatAllocContext()

	return nil
}

func (h *ConnHandler) OnCreateStream(timestamp uint32, cmd *rtmpmsg.NetConnectionCreateStream) error {
	h.log.Info("OnCreateStream: %#v", cmd)
	return nil
}

func (h *ConnHandler) OnPublish(timestamp uint32, cmd *rtmpmsg.NetStreamPublish) (err error) {
	h.log.Info("OnPublish: %#v", cmd)

	if cmd.PublishingName == "" {
		return errors.New("PublishingName is empty")
	}
	// Authenticate
	auth := strings.SplitN(cmd.PublishingName, "-", 2)
	u64, err := strconv.ParseUint(auth[0], 10, 32)

	if err != nil {
		return err
	}
	h.channelID = ftl.ChannelID(u64)
	h.streamKey = []byte(auth[1])

	if err := h.manager.NewStream(h.channelID); err != nil {
		return err
	}
	if err := h.manager.Authenticate(h.channelID, h.streamKey); err != nil {
		return err
	}

	stream, err := h.manager.StartStream(h.channelID)
	if err != nil {
		return err
	}

	h.stream = stream
	h.streamID = stream.streamID

	// Add some meta info to the logger
	h.log = h.log.WithFields(logrus.Fields{
		"channel_id": h.channelID,
		"stream_id":  h.streamID,
	})

	h.authenticated = true
	h.sequencer = rtp.NewFixedSequencer(0) // ftl client says this should be changed to a random value
	h.videoPacketizer = rtp.NewPacketizer(1392, 96, uint32(h.channelID+1), &codecs.H264Payloader{}, h.sequencer, h.videoClockRate)
	h.audioPacketizer = rtp.NewPacketizer(1392, 97, uint32(h.channelID), &codecs.OpusPayloader{}, h.sequencer, h.audioClockRate)

	h.audioEncoder, err = opus.NewEncoder(int(h.audioClockRate), 2, opus.AppAudio)
	if err != nil {
		return err
	}
	h.audioDecoder = fdkaac.NewAacDecoder()

	return nil
}

func (h *ConnHandler) OnClose() {
	h.log.Info("OnClose")

	h.quitTimer <- true

	if err := h.manager.StopStream(h.channelID); err != nil {
		panic(err)
	}

	if err := h.manager.RemoveStream(h.channelID); err != nil {
		panic(err)
	}

	if h.audioDecoder != nil {
		h.audioDecoder.Close()
		h.audioDecoder = nil
	}
}

func (h *ConnHandler) OnAudio(timestamp uint32, payload io.Reader) error {
	// return nil

	// Convert AAC to opus
	// https://github.com/notedit/rtc-rtmp - Has c bindings

	var audio flvtag.AudioData
	if err := flvtag.DecodeAudioData(payload, &audio); err != nil {
		return err
	}

	data := new(bytes.Buffer)
	if _, err := io.Copy(data, audio.Data); err != nil {
		return err
	}
	bytes := data.Bytes()

	if audio.AACPacketType == tag.AACPacketTypeSequenceHeader {
		h.log.Infof("Created new codec %s", hex.EncodeToString(bytes))
		err := h.audioDecoder.InitRaw(bytes)

		if err != nil {
			h.log.WithError(err).Errorf("error initialising stream")
			return fmt.Errorf("can't initilise codec with %s", hex.EncodeToString(bytes))
		}

		return nil
	}

	pcm, err := h.audioDecoder.Decode(bytes)
	if err != nil {
		h.log.Errorf("decode error: %s %s", hex.EncodeToString(bytes), err)
		return fmt.Errorf("decode error")
	}

	h.audioPackets++

	// h.log.Info("A=", timestamp, h.audioTimestamp/48)

	blockSize := 960
	for h.audioBuffer = append(h.audioBuffer, pcm...); len(h.audioBuffer) >= blockSize*4; h.audioBuffer = h.audioBuffer[blockSize*4:] {
		pcm16 := make([]int16, blockSize*2)
		for i := 0; i < len(pcm16); i++ {
			pcm16[i] = int16(binary.LittleEndian.Uint16(h.audioBuffer[i*2:]))
		}
		bufferSize := 1024
		opusData := make([]byte, bufferSize)
		n, err := h.audioEncoder.Encode(pcm16, opusData)
		if err != nil {
			return err
		}
		opusOutput := opusData[:n]

		packets := h.audioPacketizer.Packetize(opusOutput, uint32(blockSize))

		for _, p := range packets {
			p.Header.Timestamp = h.audioTimestamp

			if err := h.stream.WriteRTP(p); err != nil {
				return err
			}
		}
		h.audioTimestamp = h.audioTimestamp + uint32(blockSize)
	}
	return nil
}

// GStreamer -- AudioData: {SoundFormat:7 SoundRate:0 SoundSize:1 SoundType:0 AACPacketType:0 Data:0x1400022c2a0}
// OBS -- AudioData: {SoundFormat:10 SoundRate:3 SoundSize:1 SoundType:1 AACPacketType:1 Data:0x140001be380}

const (
	headerLengthField = 4
	spsId             = 0x67
	ppsId             = 0x68
)

func (h *ConnHandler) OnVideo(timestamp uint32, payload io.Reader) error {
	var video flvtag.VideoData
	if err := flvtag.DecodeVideoData(payload, &video); err != nil {
		return err
	}

	// video.CodecID == H264, I wonder if we should check this?
	// video.FrameType does not seem to contain b-frames even if they exist

	switch video.FrameType {
	case flvtag.FrameTypeKeyFrame:
		h.lastKeyFrames += 1
	case flvtag.FrameTypeInterFrame:
		h.lastInterFrames += 1
	default:
		h.log.Debug("Unknown FLV Video Frame: %+v\n", video)
	}

	data := new(bytes.Buffer)
	if _, err := io.Copy(data, video.Data); err != nil {
		return err
	}

	// From: https://github.com/Sean-Der/rtmp-to-webrtc/blob/master/rtmp.go#L110-L123
	// For example, you want to store a H.264 file and decode it on another computer. The decoder has no idea on how
	// to search the boundaries of the NAL units. So, a three-byte or four-byte start code, 0x000001 or 0x00000001,
	// is added at the beginning of each NAL unit. They are called Byte-Stream Format. Hence, the decoder can now
	// identify the boundaries easily.
	// Source: https://yumichan.net/video-processing/video-compression/introduction-to-h264-nal-unit/

	//outBuf := h.appendNALHeader(video, data.Bytes())
	outBuf := h.appendNALHeaderSpecial(video, data.Bytes())

	// Likely there's more than one set of RTP packets in this read
	samples := uint32(len(outBuf)) + h.videoClockRate
	packets := h.videoPacketizer.Packetize(outBuf, samples)

	h.videoPackets += len(packets)

	// So the video is jittering, especially on screens with no movement.
	// To reproduce, play clock scene, see it working, and then switch to desktop scene
	// You can see in the chrome inspector that the keyframes decoded drops. You can also see the weird video problems
	// happen on the clock scene, which show up in the keyframe decoded drops.
	// I want to log the keyframes tomorrow to see if we stop getting them on the ingest side.
	// Also there's some bullshit in Janus to look at

	// Pion WebRTC also provides a SampleBuilder. This consumes RTP packets and returns samples. It can be used to
	// re-order and delay for lossy streams. You can see its usage in this example in daf27b.
	// https://github.com/pion/webrtc/issues/1652
	// https://github.com/pion/rtsp-bench/blob/master/server/main.go
	// https://github.com/pion/obs-wormhole/blob/master/internal/rtmp/rtmp.go

	//start := time.Now()
	// h.log.Info("V=", timestamp)
	for _, p := range packets {
		p.Header.Timestamp = timestamp
		if err := h.stream.WriteRTP(p); err != nil {
			return err
		}
	}
	//elapsed := time.Since(start)
	//h.log.Infof("WriteRTP's took %s for %d packets", elapsed, len(packets))

	return nil
}

//func (h *ConnHandler) appendNALHeader(video flvtag.VideoData, videoBuffer []byte) []byte {
//	var outBuf []byte
//	for offset := 0; offset < len(videoBuffer); {
//		bufferLength := int(binary.BigEndian.Uint32(videoBuffer[offset : offset+headerLengthField]))
//		if offset+bufferLength >= len(videoBuffer) {
//			break
//		}
//
//		offset += headerLengthField
//		outBuf = append(outBuf, []byte{0x00, 0x00, 0x00, 0x01}...)
//		outBuf = append(outBuf, videoBuffer[offset:offset+bufferLength]...)
//
//		offset += bufferLength
//	}
//	return outBuf
//}
func (h *ConnHandler) appendNALHeaderSpecial(video flvtag.VideoData, videoBuffer []byte) []byte {
	hasSpsPps := false
	var outBuf []byte

	if video.AVCPacketType == flvtag.AVCPacketTypeNALU {
		for offset := 0; offset < len(videoBuffer); {

			bufferLength := int(binary.BigEndian.Uint32(videoBuffer[offset : offset+headerLengthField]))
			if offset+bufferLength >= len(videoBuffer) {
				break
			}

			offset += headerLengthField

			if videoBuffer[offset] == spsId {
				hasSpsPps = true
				h.sps = append(annexBPrefix(), videoBuffer[offset:offset+bufferLength]...)
			} else if videoBuffer[offset] == ppsId {
				hasSpsPps = true
				h.pps = append(annexBPrefix(), videoBuffer[offset:offset+bufferLength]...)
			}

			outBuf = append(outBuf, annexBPrefix()...)
			outBuf = append(outBuf, videoBuffer[offset:offset+bufferLength]...)

			offset += bufferLength
		}
	} else if video.AVCPacketType == flvtag.AVCPacketTypeSequenceHeader {
		const spsCountOffset = 5
		spsCount := videoBuffer[spsCountOffset] & 0x1F
		offset := 6
		h.sps = []byte{}
		for i := 0; i < int(spsCount); i++ {
			spsLen := binary.BigEndian.Uint16(videoBuffer[offset : offset+2])
			offset += 2
			if videoBuffer[offset] != spsId {
				// panic("Failed to parse SPS")
				return []byte{}
			}
			h.sps = append(h.sps, annexBPrefix()...)
			h.sps = append(h.sps, videoBuffer[offset:offset+int(spsLen)]...)
			offset += int(spsLen)
		}
		ppsCount := videoBuffer[offset]
		offset++
		for i := 0; i < int(ppsCount); i++ {
			ppsLen := binary.BigEndian.Uint16(videoBuffer[offset : offset+2])
			offset += 2
			if videoBuffer[offset] != ppsId {
				// panic("Failed to parse PPS")
				return []byte{}
			}
			h.sps = append(h.sps, annexBPrefix()...)
			h.sps = append(h.sps, videoBuffer[offset:offset+int(ppsLen)]...)
			offset += int(ppsLen)
		}
		return nil
	}

	// We have an unadorned keyframe, append SPS/PPS
	if video.FrameType == flvtag.FrameTypeKeyFrame && !hasSpsPps {
		outBuf = append(append(h.sps, h.pps...), outBuf...)
	}

	return outBuf
}

func (h *ConnHandler) setupDebug() {
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				h.log.WithFields(logrus.Fields{
					"keyframes":   h.lastKeyFrames,
					"interframes": h.lastInterFrames,
					"packets":     h.videoPackets - h.lastVideoPackets,
				}).Debug("Processed 5s of input frames from RTMP input")
				//h.log.Debugf("Processed %d video packets in the last 5 seconds (%d packets/sec)", h.videoPackets-h.lastVideoPackets, (h.videoPackets-h.lastVideoPackets)/5)
				//h.log.Debugf("KeyFrames: %d (%d frames/sec) InterFrames: %d (%d frames/sec) ", h.lastKeyFrames, h.lastKeyFrames/5, h.lastInterFrames, h.lastInterFrames/5)

				h.lastVideoPackets = h.videoPackets
				h.lastKeyFrames = 0
				h.lastInterFrames = 0
			case <-h.quitTimer:
				ticker.Stop()
				return
			}
		}
	}()
}

//func isKeyFrame(data []byte) bool {
//	const typeSTAPA = 24
//
//	var word uint32
//
//	payload := bytes.NewReader(data)
//	err := binary.Read(payload, binary.BigEndian, &word)
//
//	if err != nil || (word&0x1F000000)>>24 != typeSTAPA {
//		return false
//	}
//
//	return word&0x1F == 7
//}

func annexBPrefix() []byte {
	return []byte{0x00, 0x00, 0x00, 0x01}
}

// Other OnVideo implementation, not sure what the byte manipulation is doing
// Lost the source on this one, but I'm pretty sure it's Sean-Der on GitHub
//
//func (h *ConnHandler) OnVideo(timestamp uint32, payload io.Reader) error {
//	if h.mediaConn == nil {
//		// Don't need to do anything with this packet since we're not relaying it
//		return nil
//	}
//
//	var video flvtag.VideoData
//	if err := flvtag.DecodeVideoData(payload, &video); err != nil {
//		return err
//	}
//
//	data := new(bytes.Buffer)
//	if _, err := io.Copy(data, video.Data); err != nil {
//		return err
//	}
//
//	hasSpsPps := false
//	var outBuf []byte
//	videoBuffer := data.Bytes()
//
//	if video.AVCPacketType == flvtag.AVCPacketTypeNALU {
//		for offset := 0; offset < len(videoBuffer); {
//
//			bufferLength := int(binary.BigEndian.Uint32(videoBuffer[offset : offset+headerLengthField]))
//			if offset+bufferLength >= len(videoBuffer) {
//				break
//			}
//
//			offset += headerLengthField
//
//			if videoBuffer[offset] == spsId {
//				hasSpsPps = true
//				h.sps = append(annexBPrefix(), videoBuffer[offset:offset+bufferLength]...)
//			} else if videoBuffer[offset] == ppsId {
//				hasSpsPps = true
//				h.pps = append(annexBPrefix(), videoBuffer[offset:offset+bufferLength]...)
//			}
//
//			outBuf = append(outBuf, annexBPrefix()...)
//			outBuf = append(outBuf, videoBuffer[offset:offset+bufferLength]...)
//
//			offset += bufferLength
//		}
//	} else if video.AVCPacketType == flvtag.AVCPacketTypeSequenceHeader {
//		const spsCountOffset = 5
//		spsCount := videoBuffer[spsCountOffset] & 0x1F
//		offset := 6
//		h.sps = []byte{}
//		for i := 0; i < int(spsCount); i++ {
//			spsLen := binary.BigEndian.Uint16(videoBuffer[offset : offset+2])
//			offset += 2
//			if videoBuffer[offset] != spsId {
//				panic("Failed to parse SPS")
//			}
//			h.sps = append(h.sps, annexBPrefix()...)
//			h.sps = append(h.sps, videoBuffer[offset:offset+int(spsLen)]...)
//			offset += int(spsLen)
//		}
//		ppsCount := videoBuffer[offset]
//		offset++
//		for i := 0; i < int(ppsCount); i++ {
//			ppsLen := binary.BigEndian.Uint16(videoBuffer[offset : offset+2])
//			offset += 2
//			if videoBuffer[offset] != ppsId {
//				panic("Failed to parse PPS")
//			}
//			h.sps = append(h.sps, annexBPrefix()...)
//			h.sps = append(h.sps, videoBuffer[offset:offset+int(ppsLen)]...)
//			offset += int(ppsLen)
//		}
//		return nil
//	}
//
//	// We have an unadorned keyframe, append SPS/PPS
//	if video.FrameType == flvtag.FrameTypeKeyFrame && !hasSpsPps {
//		outBuf = append(append(h.sps, h.pps...), outBuf...)
//	}
//
//	// Take the outbuf, shove it into some rtp packets
//	// Then later when a relay is setup, send those rtp packets through
//
//	sample := media.Sample{
//		Data:     outBuf,
//		Duration: time.Second / 30,
//	}
//	samples := uint32(sample.Duration.Seconds() + float64(h.clockRate))
//	packets := h.packetizer.(rtp.Packetizer).Packetize(sample.Data, samples)
//
//	for _, p := range packets {
//		p.PayloadType = 96
//		p.SSRC = 123456790
//		buf, err := p.Marshal()
//		if err != nil {
//			return err
//		}
//		if _, err = h.mediaConn.Write(buf); err != nil {
//			return err
//		}
//	}
//
//	return nil
//}
