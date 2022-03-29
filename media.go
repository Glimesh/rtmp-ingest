package main

import (
	"encoding/binary"

	flvtag "github.com/yutopp/go-flv/tag"
)

const (
	headerLengthField = 4
	spsId             = 0x67
	ppsId             = 0x68
)

var (
	annexBPrefix = []byte{0x00, 0x00, 0x00, 0x01}
)

func appendNALHeaderSpecial(video flvtag.VideoData, videoBuffer []byte) (sps, pps, outbuf []byte) {
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
				sps = append(annexBPrefix, videoBuffer[offset:offset+bufferLength]...)
			} else if videoBuffer[offset] == ppsId {
				hasSpsPps = true
				pps = append(annexBPrefix, videoBuffer[offset:offset+bufferLength]...)
			}

			outBuf = append(outBuf, annexBPrefix...)
			outBuf = append(outBuf, videoBuffer[offset:offset+bufferLength]...)

			offset += bufferLength
		}
	} else if video.AVCPacketType == flvtag.AVCPacketTypeSequenceHeader {
		const spsCountOffset = 5
		spsCount := videoBuffer[spsCountOffset] & 0x1F
		offset := 6
		sps = []byte{}
		for i := 0; i < int(spsCount); i++ {
			spsLen := binary.BigEndian.Uint16(videoBuffer[offset : offset+2])
			offset += 2
			if videoBuffer[offset] != spsId {
				// panic("Failed to parse SPS")
				return
			}
			sps = append(sps, annexBPrefix...)
			sps = append(sps, videoBuffer[offset:offset+int(spsLen)]...)
			offset += int(spsLen)
		}
		ppsCount := videoBuffer[offset]
		offset++
		for i := 0; i < int(ppsCount); i++ {
			ppsLen := binary.BigEndian.Uint16(videoBuffer[offset : offset+2])
			offset += 2
			if videoBuffer[offset] != ppsId {
				// panic("Failed to parse PPS")
				return
			}
			sps = append(sps, annexBPrefix...)
			sps = append(sps, videoBuffer[offset:offset+int(ppsLen)]...)
			offset += int(ppsLen)
		}
		return
	}

	// We have an unadorned keyframe, append SPS/PPS
	if video.FrameType == flvtag.FrameTypeKeyFrame && !hasSpsPps {
		outBuf = append(append(sps, pps...), outBuf...)
	}

	return sps, pps, outBuf
}
