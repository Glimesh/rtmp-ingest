package h264

import (
	"image"
	"image/color"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestH264Decode(t *testing.T) {
	assert := assert.New(t)

	file, err := os.ReadFile("example.h264")
	if err != nil {
		t.Error(err)
	}

	h264dec, err := NewH264Decoder()
	defer h264dec.Close()
	if err != nil {
		t.Error(err)
	}
	img, err := h264dec.Decode(file)
	if err != nil {
		t.Error(err)
	}

	assert.Equal(img.At(0, 0), color.RGBA(color.RGBA{R: 0x2f, G: 0x12, B: 0xe, A: 0xff}))
	assert.Equal(img.Bounds(), image.Rectangle{Max: image.Point{X: 1280, Y: 720}})
}
