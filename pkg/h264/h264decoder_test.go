package h264

import (
	"testing"
)

func TestH264Decode(t *testing.T) {

	// nal, err := hex.DecodeString("0000 0109 1000 0001 6742 0020 e900 800c 3200 0001 68ce 3c80 0000 0001 6588 801a")
	// if err != nil {
	// 	t.Error(err)
	// }

	// dec, err := NewH264Decoder(nal)
	// if err != nil {
	// 	t.Error(err)
	// }

	// fmt.Printf("%v", dec)

	// for i, n := range nal[1:] {
	// 	img, err := dec.Decode(n)

	// 	if err == nil {
	// 		fp, _ := os.Create(fmt.Sprintf("/tmp/dec-%d.jpg", i))
	// 		jpeg.Encode(fp, img, nil)
	// 		fp.Close()
	// 	}
	// }

}
