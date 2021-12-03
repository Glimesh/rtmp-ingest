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

	"github.com/Glimesh/go-fdkaac/fdkaac"
	"github.com/Glimesh/rtmp-ingest/pkg/protocols/ftl"
	"github.com/pion/rtp/v2"
	"github.com/pion/rtp/v2/codecs"
	"github.com/sirupsen/logrus"
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
				Logger: log.WithField("app", "yutopp/go-rtmp"),
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

	audioSequencer  rtp.Sequencer
	videoSequencer  rtp.Sequencer
	videoPacketizer rtp.Packetizer
	audioPacketizer rtp.Packetizer
	videoClockRate  uint32
	audioClockRate  uint32

	lastKeyFrames   int
	lastInterFrames int

	sps []byte
	pps []byte

	quitTimer    chan bool
	audioDecoder *fdkaac.AacDecoder
	audioBuffer  []byte
	audioEncoder *opus.Encoder

	// Metadata
	startTime           int64
	lastTime            int64 // Last time the metadata collector ran
	audioBps            int
	videoBps            int
	audioPackets        int
	videoPackets        int
	lastAudioPackets    int
	lastVideoPackets    int
	clientVendorName    string
	clientVendorVersion string
	videoCodec          string
	audioCodec          string
	// Wont be able to get these until we can decode the h264 packets.
	videoHeight int
	videoWidth  int
}

func (h *ConnHandler) OnServe(conn *rtmp.Conn) {
	h.log.Info("OnServe: %#v", conn)
}

func (h *ConnHandler) OnConnect(timestamp uint32, cmd *rtmpmsg.NetConnectionConnect) (err error) {
	h.log.Info("OnConnect: %#v", cmd)

	h.videoClockRate = 90000
	// TODO: This can be customized by the user, we should figure out how to infer it from the client
	h.audioClockRate = 48000

	h.startTime = time.Now().Unix()

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
		h.log.Error(err)
		return err
	}
	if err := h.manager.Authenticate(h.channelID, h.streamKey); err != nil {
		h.log.Error(err)
		return err
	}

	stream, err := h.manager.StartStream(h.channelID)
	if err != nil {
		h.log.Error(err)
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
	h.videoSequencer = rtp.NewFixedSequencer(25000)
	h.videoPacketizer = rtp.NewPacketizer(1392, 96, uint32(h.channelID+1), &codecs.H264Payloader{}, h.videoSequencer, h.videoClockRate)

	if err := h.initAudio(h.audioClockRate); err != nil {
		return err
	}

	go h.setupMetadataCollector()

	return nil
}

const USEC_IN_SEC = 1000000

func (h *ConnHandler) initAudio(clockRate uint32) (err error) {
	h.audioSequencer = rtp.NewFixedSequencer(0) // ftl client says this should be changed to a random value
	h.audioPacketizer = rtp.NewPacketizer(1392, 97, uint32(h.channelID), &codecs.OpusPayloader{}, h.audioSequencer, clockRate)

	h.audioEncoder, err = opus.NewEncoder(int(clockRate), 2, opus.AppAudio)
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

	if audio.AACPacketType == flvtag.AACPacketTypeSequenceHeader {
		h.log.Infof("Created new codec %s", hex.EncodeToString(bytes))
		err := h.audioDecoder.InitRaw(bytes)

		if err != nil {
			h.log.WithError(err).Errorf("error initializing stream")
			return fmt.Errorf("can't initialize codec with %s", hex.EncodeToString(bytes))
		}

		return nil
	}

	pcm, err := h.audioDecoder.Decode(bytes)
	if err != nil {
		h.log.Errorf("decode error: %s %s", hex.EncodeToString(bytes), err)
		return fmt.Errorf("decode error")
	}

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
			h.audioPackets++
			if err := h.stream.WriteRTP(p); err != nil {
				h.log.Error(err)
				return err
			}
		}
	}

	return nil
}

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

	outBuf := h.appendNALHeaderSpecial(video, data.Bytes())

	// Likely there's more than one set of RTP packets in this read
	samples := uint32(len(outBuf)) + h.videoClockRate
	packets := h.videoPacketizer.Packetize(outBuf, samples)

	for _, p := range packets {
		h.videoPackets++
		if err := h.stream.WriteRTP(p); err != nil {
			h.log.Error(err)
			return err
		}
	}

	return nil
}

func (h *ConnHandler) appendNALHeaderSpecial(video flvtag.VideoData, videoBuffer []byte) []byte {
	const (
		headerLengthField = 4
		spsId             = 0x67
		ppsId             = 0x68
	)

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

func (h *ConnHandler) setupMetadataCollector() {
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				h.lastTime = time.Now().Unix()

				h.log.WithFields(logrus.Fields{
					"keyframes":   h.lastKeyFrames,
					"interframes": h.lastInterFrames,
					"packets":     h.videoPackets - h.lastVideoPackets,
				}).Debug("Processed 5s of input frames from RTMP input")
				//h.log.Debugf("Processed %d video packets in the last 5 seconds (%d packets/sec)", h.videoPackets-h.lastVideoPackets, (h.videoPackets-h.lastVideoPackets)/5)
				//h.log.Debugf("KeyFrames: %d (%d frames/sec) InterFrames: %d (%d frames/sec) ", h.lastKeyFrames, h.lastKeyFrames/5, h.lastInterFrames, h.lastInterFrames/5)

				// Calculate some of our last fields
				h.audioBps = 


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

func annexBPrefix() []byte {
	return []byte{0x00, 0x00, 0x00, 0x01}
}
