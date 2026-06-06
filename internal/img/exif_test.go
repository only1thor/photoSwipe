package img

import (
	"image"
	"image/color"
	"testing"
)

func TestApplyOrientation_PassThroughForUnsetOrOne(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for _, o := range []int{0, 1, 9, -1} {
		out := ApplyOrientation(src, o)
		if out != image.Image(src) {
			t.Fatalf("orientation %d: expected pass-through", o)
		}
	}
}

func TestApplyOrientation_Rotate90CW(t *testing.T) {
	// 2x3 image; top-left pixel is red. After orientation=6 (rotate 90 CW),
	// the original top-left becomes top-right of a 3x2 image.
	src := image.NewRGBA(image.Rect(0, 0, 2, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 2; x++ {
			src.Set(x, y, color.RGBA{B: uint8(x*100 + y), A: 255})
		}
	}
	src.Set(0, 0, color.RGBA{R: 255, A: 255})

	out := ApplyOrientation(src, 6)
	b := out.Bounds()
	if b.Dx() != 3 || b.Dy() != 2 {
		t.Fatalf("rotated dims = %dx%d, want 3x2", b.Dx(), b.Dy())
	}
	// Top-right of rotated should be red.
	r, _, _, _ := out.At(2, 0).RGBA()
	if r>>8 != 255 {
		t.Fatalf("expected red at (2,0); got %v", out.At(2, 0))
	}
}

func TestReadExifFile_NonJPEGReturnsZero(t *testing.T) {
	info := ReadExifFile("exif.go") // source file, not an image
	if info.Orientation != 0 || !info.DateTimeOriginal.IsZero() {
		t.Fatalf("non-JPEG should return zero ExifInfo, got %+v", info)
	}
}
