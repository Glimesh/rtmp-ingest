package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/clone1018/rtmp-ingest/pkg/orchestrator"
	"github.com/clone1018/rtmp-ingest/pkg/services"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/sirupsen/logrus"
	flvtag "github.com/yutopp/go-flv/tag"
	"github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"
)

func NewRTMPServer(service services.Service, orch *orchestrator.Client, log logrus.FieldLogger) {
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
					orch:    orch,
					service: service,
					log:     log,
				},

				ControlState: rtmp.StreamControlStateConfig{
					DefaultBandwidthWindowSize: 6 * 1024 * 1024 / 8,
				},
			}
		},
	})
	if err := srv.Serve(listener); err != nil {
		log.Fatal(err)
	}
}

type ConnHandler struct {
	rtmp.DefaultHandler
	orch    *orchestrator.Client
	service services.Service

	log logrus.FieldLogger

	channelID     uint32
	streamID      uint32
	streamKey     []byte
	authenticated bool

	//mediaConn net.Conn
	rtpWriter io.Writer

	sequencer  rtp.Sequencer
	packetizer rtp.Packetizer
	clockRate  uint32

	videoPackets     int
	lastVideoPackets int

	lastKeyFrames   int
	lastInterFrames int

	sps []byte
	pps []byte

	quitTimer chan bool
}

func (h *ConnHandler) OnServe(conn *rtmp.Conn) {
	h.log.Info("OnServe: %#v", conn)
	h.clockRate = 90000

	h.sequencer = rtp.NewRandomSequencer()
	h.packetizer = rtp.NewPacketizer(1392, 0, 0, &codecs.H264Payloader{}, h.sequencer, h.clockRate)
}

func (h *ConnHandler) OnConnect(timestamp uint32, cmd *rtmpmsg.NetConnectionConnect) (err error) {
	h.log.Info("OnConnect: %#v", cmd)

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
	h.channelID = uint32(u64)
	h.streamKey = []byte(auth[1])

	actualKey, err := h.service.GetHmacKey(h.channelID)
	if err != nil {
		return err
	}
	if string(h.streamKey) != string(actualKey) {
		return errors.New("incorrect stream key")
	}

	// Authentication passed, start stream
	h.streamID, err = h.service.StartStream(h.channelID)
	if err != nil {
		return err
	}

	h.orch.SendStreamPublishing(orchestrator.StreamPublishingMessage{
		Context:   1,
		ChannelID: h.channelID,
		StreamID:  h.streamID,
	})

	err = streamManager.AddStream(h)
	if err != nil {
		return err
	}

	// Add some meta info to the logger
	h.log = h.log.WithFields(logrus.Fields{
		"channel_id": h.channelID,
		"stream_id":  h.streamID,
	})

	h.authenticated = true

	ticker := time.NewTicker(5 * time.Second)
	h.quitTimer = make(chan bool)
	go func() {
		for {
			select {
			case <-ticker.C:
				h.log.WithFields(logrus.Fields{
					"keyframes": h.lastKeyFrames,
					"interframes": h.lastInterFrames,
					"packets": h.videoPackets-h.lastVideoPackets,
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

	return nil
}

func (h *ConnHandler) OnClose() {
	h.log.Info("OnClose")

	h.quitTimer <- true

	// if !h.authenticated {
	// 	// We never published anything to end
	// 	return
	// }

	// Tell the orchestrator the stream has ended
	h.orch.SendStreamPublishing(orchestrator.StreamPublishingMessage{
		Context:   0,
		ChannelID: h.channelID,
		StreamID:  h.streamID,
	})

	// Tell the service the stream has ended
	h.service.EndStream(h.streamID)

	streamManager.RemoveStream(h.channelID)
}

func (h *ConnHandler) OnAudio(timestamp uint32, payload io.Reader) error {
	// Enabling audio causes janus to *freak out*
	// I guess because we're sending AAC when its reading Opus :)
	return nil

	//if h.rtpWriter == nil {
	//	// Don't need to do anything with this packet since we're not ready to relay it
	//	return nil
	//}
	//
	//var audio flvtag.AudioData
	//if err := flvtag.DecodeAudioData(payload, &audio); err != nil {
	//	return err
	//}
	//
	//data := new(bytes.Buffer)
	//if _, err := io.Copy(data, audio.Data); err != nil {
	//	return err
	//}
	//outBuf := data.Bytes()
	//
	//samples := uint32(len(outBuf)) + h.clockRate
	//packets := h.packetizer.Packetize(outBuf, samples)
	//
	//for _, p := range packets {
	//	p.PayloadType = 97
	//	p.SSRC = h.channelID
	//	buf, err := p.Marshal()
	//	if err != nil {
	//		return err
	//	}
	//	if _, err = h.rtpWriter.Write(buf); err != nil {
	//		return err
	//	}
	//}
	//
	//return nil
}

// GStreamer -- AudioData: {SoundFormat:7 SoundRate:0 SoundSize:1 SoundType:0 AACPacketType:0 Data:0x1400022c2a0}
// OBS -- AudioData: {SoundFormat:10 SoundRate:3 SoundSize:1 SoundType:1 AACPacketType:1 Data:0x140001be380}

const (
	headerLengthField = 4
	spsId             = 0x67
	ppsId             = 0x68
)

func (h *ConnHandler) OnVideo(timestamp uint32, payload io.Reader) error {
	if h.rtpWriter == nil {
		// Don't need to do anything with this packet since we're not ready to relay it
		return nil
	}

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

	outBuf := h.appendNALHeader(video, data.Bytes())
	// outBuf := h.appendNALHeaderSpecial(video, data.Bytes())

	// Likely there's more than one set of RTP packets in this read
	samples := uint32(len(outBuf)) + h.clockRate
	packets := h.packetizer.Packetize(outBuf, samples)

	h.videoPackets += len(packets)

	// So the video is jittering, especially on screens with no movement.
	// To reproduce, play clock scene, see it working, and then switch to desktop scene
	// You can see in the chrome inspector that the keyframes decoded drops. You can also see the weird video problems
	// happen on the clock scene, which show up in the keyframe decoded drops.
	// I want to log the keyframes tomorrow to see if we stop getting them on the ingest side.
	// Also there's some bullshit in Janus to look at

	for _, p := range packets {
		p.PayloadType = 96
		p.SSRC = h.channelID + 1
		buf, err := p.Marshal()
		if err != nil {
			return err
		}

		if _, err = h.rtpWriter.Write(buf); err != nil {
			return err
		}
	}

	return nil
}

func (h *ConnHandler) appendNALHeader(video flvtag.VideoData, videoBuffer []byte) []byte {
	var outBuf []byte
	for offset := 0; offset < len(videoBuffer); {
		bufferLength := int(binary.BigEndian.Uint32(videoBuffer[offset : offset+headerLengthField]))
		if offset+bufferLength >= len(videoBuffer) {
			break
		}

		offset += headerLengthField
		outBuf = append(outBuf, []byte{0x00, 0x00, 0x00, 0x01}...)
		outBuf = append(outBuf, videoBuffer[offset:offset+bufferLength]...)

		offset += bufferLength
	}
	return outBuf
}
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
