// Package dhash implements the difference-hash perceptual fingerprint.
//
// dHash works by:
//  1. resizing the image to 9×8 grayscale,
//  2. for each row, comparing each pixel to its right neighbour,
//  3. emitting one bit per comparison (1 if left brighter than right),
//  4. packing the 64 bits into a uint64.
//
// Hashes can be compared with Distance (Hamming distance). Near-duplicates
// typically differ in fewer than ~10 bits out of 64; this is the threshold
// surfaced as Settings.DupeThreshold.
package dhash

import (
	"image"
	"math/bits"

	"golang.org/x/image/draw"
)

// Compute returns the dHash fingerprint of img.
func Compute(img image.Image) uint64 {
	const w, h = 9, 8
	gray := image.NewGray(image.Rect(0, 0, w, h))
	draw.BiLinear.Scale(gray, gray.Bounds(), img, img.Bounds(), draw.Src, nil)

	var hash uint64
	var bit uint64 = 1
	for y := 0; y < h; y++ {
		row := gray.PixOffset(0, y)
		for x := 0; x < w-1; x++ {
			if gray.Pix[row+x] > gray.Pix[row+x+1] {
				hash |= bit
			}
			bit <<= 1
		}
	}
	return hash
}

// Distance returns the Hamming distance between two dHash fingerprints —
// the number of bits that differ. Range: 0 (identical) to 64 (opposite).
func Distance(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}
