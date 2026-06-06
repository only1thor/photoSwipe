package dhash

import (
	"image"
	"image/color"
	"math/rand"
	"testing"
)

func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / w), uint8(y * 255 / h), 128, 255})
		}
	}
	return img
}

func noisy(w, h int, seed int64) *image.RGBA {
	r := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(r.Intn(256)), uint8(r.Intn(256)), uint8(r.Intn(256)), 255})
		}
	}
	return img
}

func TestDistanceSelfIsZero(t *testing.T) {
	g := gradient(80, 60)
	h := Compute(g)
	if d := Distance(h, h); d != 0 {
		t.Fatalf("distance(h,h)=%d, want 0", d)
	}
}

func TestNearDuplicatesAreClose(t *testing.T) {
	a := gradient(160, 120)
	b := gradient(160, 120)
	for i := 0; i < 50; i++ {
		x, y := i%160, i%120
		c := b.RGBAAt(x, y)
		c.R = uint8(int(c.R)+15) & 0xff
		b.SetRGBA(x, y, c)
	}
	ha, hb := Compute(a), Compute(b)
	d := Distance(ha, hb)
	if d > 8 {
		t.Fatalf("near-duplicate distance=%d, want <=8", d)
	}
}

func TestDifferentImagesAreFar(t *testing.T) {
	a := noisy(120, 90, 1)
	b := noisy(120, 90, 2)
	d := Distance(Compute(a), Compute(b))
	if d < 32 {
		t.Fatalf("random noise distance=%d, want >=32 (likely unrelated)", d)
	}
}

func TestDownscaleInvariance(t *testing.T) {
	big := gradient(640, 480)
	small := gradient(160, 120)
	d := Distance(Compute(big), Compute(small))
	if d > 4 {
		t.Fatalf("resolution-invariant distance=%d, want <=4", d)
	}
}

func TestZero(t *testing.T) {
	if !(Hash{}).Zero() {
		t.Fatal("Hash{}.Zero() should be true")
	}
	if (Hash{H: 1}).Zero() {
		t.Fatal("non-zero hash should not be Zero")
	}
}
