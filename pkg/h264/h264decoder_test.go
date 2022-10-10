package h264

import (
	"fmt"
	"image"
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
	if err != nil {
		t.Error(err)
	}
	defer h264dec.Close()
	img, err := h264dec.Decode(file)
	if err != nil {
		t.Error(err)
	}

	// AVFrame pict_type values
	// FF_I_TYPE            = 1         #< Intra
	// FF_P_TYPE            = 2         #< Predicted
	// FF_B_TYPE            = 3         #< Bi-dir predicted
	// FF_S_TYPE            = 4         #< S(GMC)-VOP MPEG4
	// FF_SI_TYPE           = 5         #< Switching Intra
	// FF_SP_TYPE           = 6         #< Switching Predicte
	// FF_BI_TYPE           = 7
	fmt.Printf("PictType: %v\n", h264dec.srcFrame.pict_type)

	// I don't think color comparison works as well for potentially compressed video...
	// assert.Equal(img.At(0, 0), color.RGBA(color.RGBA{R: 0x2f, G: 0x12, B: 0xe, A: 0xff}))
	assert.Equal(img.Bounds(), image.Rectangle{Max: image.Point{X: 1280, Y: 720}})
}
