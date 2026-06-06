// Package dhash implements a combined horizontal+vertical difference-hash
// perceptual fingerprint.
//
// The image is resized to 9×9 grayscale, then two 64-bit hashes are emitted:
//
//   - H: for each of 8 rows, compare each pixel to its right neighbour.
//   - V: for each of 8 columns, compare each pixel to its bottom neighbour.
//
// 128 bits total. Doubling the bits roughly doubles the Hamming distance
// scale but — because horizontal and vertical edge structure are largely
// independent for unrelated photos — sharpens the gap between near-dupes
// (small distance on both axes) and "similar but distinct" shots (random
// on at least one axis). Empirically this is what makes phone-shot pairs
// of the same scene at different tilts/crops separable from unrelated
// shots of similar tonal range.
package dhash

import (
	"image"
	"math/bits"

	"golang.org/x/image/draw"
)

// Hash is the combined H+V difference-hash fingerprint.
type Hash struct {
	H, V uint64
}

// Zero reports whether h is the zero value — i.e. "not yet hashed".
func (h Hash) Zero() bool { return h.H == 0 && h.V == 0 }

// Compute returns the dHash fingerprint of img.
func Compute(img image.Image) Hash {
	const w, h = 9, 9
	gray := image.NewGray(image.Rect(0, 0, w, h))
	draw.BiLinear.Scale(gray, gray.Bounds(), img, img.Bounds(), draw.Src, nil)

	var hh, hv uint64
	var bit uint64 = 1
	for y := 0; y < 8; y++ {
		row := gray.PixOffset(0, y)
		for x := 0; x < 8; x++ {
			if gray.Pix[row+x] > gray.Pix[row+x+1] {
				hh |= bit
			}
			bit <<= 1
		}
	}
	bit = 1
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			if gray.Pix[gray.PixOffset(x, y)] > gray.Pix[gray.PixOffset(x, y+1)] {
				hv |= bit
			}
			bit <<= 1
		}
	}
	return Hash{H: hh, V: hv}
}

// Distance returns the Hamming distance between two fingerprints — the
// number of bits that differ across both planes. Range: 0..128.
func Distance(a, b Hash) int {
	return bits.OnesCount64(a.H^b.H) + bits.OnesCount64(a.V^b.V)
}
