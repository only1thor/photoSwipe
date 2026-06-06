package img

import (
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/image/draw"
)

// ThumbnailMaxSide bounds the requested thumbnail width so users can't
// trigger arbitrarily large resizes.
const ThumbnailMaxSide = 1600

// ThumbCacheDir is the directory inside the photos folder where generated
// thumbnails are cached. It starts with a dot so the scanner skips it.
const ThumbCacheDir = ".thumbs"

// ServeThumb writes a JPEG thumbnail for srcAbs into w, generating it lazily
// and caching the result in <photoDir>/.thumbs/<id>-<maxSide>.jpg.
func ServeThumb(srcAbs, photoDir, id string, maxSide int, w io.Writer) error {
	if maxSide <= 0 {
		return errors.New("maxSide must be > 0")
	}
	if maxSide > ThumbnailMaxSide {
		maxSide = ThumbnailMaxSide
	}

	cacheDir := filepath.Join(photoDir, ThumbCacheDir)
	cachePath := filepath.Join(cacheDir, fmt.Sprintf("%s-%d.jpg", id, maxSide))

	// Cache hit
	if f, err := os.Open(cachePath); err == nil {
		defer f.Close()
		_, err := io.Copy(w, f)
		return err
	}

	// Cache miss — generate
	exif := ReadExifFile(srcAbs)
	src, err := os.Open(srcAbs)
	if err != nil {
		return err
	}
	defer src.Close()
	srcImg, _, err := image.Decode(src)
	if err != nil {
		return err
	}
	srcImg = ApplyOrientation(srcImg, exif.Orientation)

	dst := scaleTo(srcImg, maxSide)

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(cacheDir, "thumb-*.jpg.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := jpeg.Encode(tmp, dst, &jpeg.Options{Quality: 80}); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, cachePath); err != nil {
		os.Remove(tmpName)
		return err
	}

	f, err := os.Open(cachePath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// PurgeThumbs removes any cached thumbnail whose name starts with id-.
// Intended for use after a file is deleted or replaced.
func PurgeThumbs(photoDir, id string) {
	cacheDir := filepath.Join(photoDir, ThumbCacheDir)
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if len(name) > len(id)+1 && name[:len(id)] == id && name[len(id)] == '-' {
			os.Remove(filepath.Join(cacheDir, name))
		}
	}
}

func scaleTo(src image.Image, maxSide int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= maxSide && sh <= maxSide {
		// Nothing to scale; just re-encode as RGBA so we can JPEG it.
		dst := image.NewRGBA(image.Rect(0, 0, sw, sh))
		draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
		return dst
	}
	var nw, nh int
	if sw >= sh {
		nw = maxSide
		nh = sh * maxSide / sw
	} else {
		nh = maxSide
		nw = sw * maxSide / sh
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return dst
}
