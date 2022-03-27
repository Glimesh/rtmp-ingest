# Various Notes
Apparently, developing RTMP => FTL is at least slightly complicated. These notes are a scattered collection of failures that might be useful in the future.

## mpeg notes
Seems like a useful resource: 
https://github.com/aler9/gortsplib/blob/0e8c93c5c2eaf7d58648998c29e221882ea5207a/examples/client-read-h264-save-to-disk/mpegtsencoder.go

## OnVideo Notes

### Simple NAL Header
It's unclear which of these implementations are correct, but here's the unused one:

```go

// From: https://github.com/Sean-Der/rtmp-to-webrtc/blob/master/rtmp.go#L110-L123
// For example, you want to store a H.264 file and decode it on another computer. The decoder has no idea on how
// to search the boundaries of the NAL units. So, a three-byte or four-byte start code, 0x000001 or 0x00000001,
// is added at the beginning of each NAL unit. They are called Byte-Stream Format. Hence, the decoder can now
// identify the boundaries easily.
// Source: https://yumichan.net/video-processing/video-compression/introduction-to-h264-nal-unit/
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
```

### IsKeyFrame
```go
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
```

### Video Jittering (resolved?)
So the video is jittering, especially on screens with no movement.
To reproduce, play clock scene, see it working, and then switch to desktop scene
You can see in the chrome inspector that the keyframes decoded drops. You can also see the weird video problems
happen on the clock scene, which show up in the keyframe decoded drops.
I want to log the keyframes tomorrow to see if we stop getting them on the ingest side.
Also there's some bullshit in Janus to look at

Pion WebRTC also provides a SampleBuilder. This consumes RTP packets and returns samples. It can be used to
re-order and delay for lossy streams. You can see its usage in this example in daf27b.
https://github.com/pion/webrtc/issues/1652
https://github.com/pion/rtsp-bench/blob/master/server/main.go
https://github.com/pion/obs-wormhole/blob/master/internal/rtmp/rtmp.go


### 2021-10-22 Luke Notes
_update_timestamp in media.c has this code
_update_timestmap is called in both media_send_video and media_send_audio
3 params: FTL stream configuration, media component (audio or video), and dts_usec
dts_usec comes from anytime ftl_ingest_send_media_dts is called, in the first example
  it comes from ftl_app/main.c which calls timeval_to_us(&frameTime)
A note from FTL: In a real app these timestamps should come from the samples!

```go
uint64_t timeval_to_us(struct timeval *tv)
{
  return tv->tv_sec * (uint64_t)1000000 + tv->tv_usec;
}

fpsDen := 1
fpsNum := 30
if video.FrameType == flvtag.FrameTypeKeyFrame {
    dst_usec_f := float32(fpsDen)*1000000.0/float32(fpsNum) + h.videoDtsError
    dts_increment_usec := uint64(dst_usec_f)
    h.videoDtsError = dst_usec_f - float32(dts_increment_usec)
    fmt.Println("Adding ", dts_increment_usec)
    h.videoDts = h.videoDts + dts_increment_usec
}

videoTimestamp := (uint64(h.videoDts) - uint64(h.startTime)) * uint64(h.videoClockRate)
actualTimestamp := uint32((videoTimestamp + USEC_IN_SEC/2) / USEC_IN_SEC)

dtsUsec := uint64(usecTimestamp())
myTimestamp := ((dtsUsec - h.startDtsUsec) * (uint64(h.videoClockRate)))
finalTimestamp := uint32((myTimestamp + USEC_IN_SEC/2) / USEC_IN_SEC)

fmt.Printf("Video DTS Diff: %d\n", uint32(actualTimestamp)-h.lastVideoTimestamp)
h.lastVideoTimestamp = uint32(actualTimestamp)

fmt.Printf("VIDEO: startTime=%d videoDts=%d dst_usec_f=%f dts_increment_usec=%d videoDtsError=%f videoTimestamp=%d actualTimestamp=%d\n", h.startTime, h.videoDts, dst_usec_f, dts_increment_usec, h.videoDtsError, videoTimestamp, actualTimestamp)
```