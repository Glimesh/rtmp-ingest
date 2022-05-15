package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Glimesh/go-fdkaac/fdkaac"
	"github.com/Glimesh/rtmp-ingest/pkg/h264"
	"github.com/Glimesh/rtmp-ingest/pkg/protocols/ftl"
	"github.com/Glimesh/rtmp-ingest/pkg/services"
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
					manager:                streamManager,
					log:                    log,
					stopMetadataCollection: make(chan bool, 1),
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

	videoSequencer  rtp.Sequencer
	videoPacketizer rtp.Packetizer
	videoClockRate  uint32

	audioSequencer  rtp.Sequencer
	audioPacketizer rtp.Packetizer
	audioClockRate  uint32
	audioDecoder    *fdkaac.AacDecoder
	audioBuffer     []byte
	audioEncoder    *opus.Encoder

	keyframes       int
	lastKeyFrames   int
	lastInterFrames int

	sps []byte
	pps []byte

	stopMetadataCollection chan bool

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
	videoHeight         int
	videoWidth          int

	lastFullFrame []byte
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
	h.audioCodec = "opus"
	h.videoCodec = "H264"
	h.videoHeight = 0
	h.videoWidth = 0

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
		h.log.Error(err)
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

	if err := h.initVideo(h.videoClockRate); err != nil {
		return err
	}
	if err := h.initAudio(h.audioClockRate); err != nil {
		return err
	}

	go h.setupMetadataCollector()

	return nil
}

func (h *ConnHandler) initVideo(clockRate uint32) (err error) {
	h.videoSequencer = rtp.NewFixedSequencer(25000)
	h.videoPacketizer = rtp.NewPacketizer(1392, 96, uint32(h.channelID+1), &codecs.H264Payloader{}, h.videoSequencer, clockRate)

	return nil
}

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

	h.stopMetadataCollection <- true

	if err := h.manager.StopStream(h.channelID); err != nil {
		h.log.Error(err)
		// panic(err)
	}

	if err := h.manager.RemoveStream(h.channelID); err != nil {
		h.log.Error(err)
		// panic(err)
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
		h.keyframes += 1
	case flvtag.FrameTypeInterFrame:
		h.lastInterFrames += 1
	default:
		h.log.Debug("Unknown FLV Video Frame: %+v\n", video)
	}

	data := new(bytes.Buffer)
	if _, err := io.Copy(data, video.Data); err != nil {
		return err
	}

	var outBuf []byte
	h.sps, h.pps, outBuf = appendNALHeaderSpecial(video, data.Bytes())

	if video.FrameType == flvtag.FrameTypeKeyFrame {
		// Save the last full keyframe for anything we may need, eg thumbnails
		h.lastFullFrame = outBuf
	}

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
				h.audioBps = 0

				h.lastVideoPackets = h.videoPackets
				h.lastKeyFrames = 0
				h.lastInterFrames = 0

				// Thumbnail generation
				// https://github.com/gen2brain/x264-go/blob/master/examples/screengrab/screengrab.go

				// This code is still buggy as hell, I suspect we're corrupting h.lastOutBuf data since we truncate
				// it outside of the main thread.
				if len(h.lastFullFrame) > 0 {
					var img image.Image
					h264dec, err := h264.NewH264Decoder()
					defer h264dec.Close()
					if err != nil {
						h.log.Error(err)
						return
					}
					img, err = h264dec.Decode(h.lastFullFrame)
					if err != nil {
						h.log.Error(err)
						return
					}

					if img != nil {
						buff := new(bytes.Buffer)
						err = jpeg.Encode(buff, img, &jpeg.Options{
							Quality: 75,
						})
						if err != nil {
							h.log.Error(err)
							return
						}

						err = h.manager.service.SendJpegPreviewImage(h.streamID, buff.Bytes())
						if err != nil {
							h.log.Error(err)
						}
						buff.Reset()

						// Also update our metadata
						h.videoWidth = img.Bounds().Dx()
						h.videoHeight = img.Bounds().Dy()
					}
				}

				err := h.manager.service.UpdateStreamMetadata(h.streamID, services.StreamMetadata{
					AudioCodec:        h.audioCodec,
					IngestServer:      h.manager.orchestrator.ClientHostname,
					IngestViewers:     0,
					LostPackets:       0, // Don't exist
					NackPackets:       0, // Don't exist
					RecvPackets:       h.videoPackets + h.audioPackets,
					SourceBitrate:     0, // Likely just need to calculate the bytes between two 5s snapshots?
					SourcePing:        0, // Not accessible unless we ping them manually
					StreamTimeSeconds: int(h.lastTime - h.startTime),
					VendorName:        h.clientVendorName,
					VendorVersion:     h.clientVendorVersion,
					VideoCodec:        h.videoCodec,
					VideoHeight:       h.videoHeight,
					VideoWidth:        h.videoWidth,
				})
				if err != nil {
					h.log.Error(err)
				}

			case <-h.stopMetadataCollection:
				ticker.Stop()
				return
			}
		}
	}()
}
