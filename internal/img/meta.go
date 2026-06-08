package img

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

type Meta struct {
	Width    int
	Height   int
	Format   string
	SizeKB   int64
	Modified time.Time
}

// Inspect returns lightweight metadata for an image file.
// It does not decode pixels, only the file header.
func Inspect(absPath string) (Meta, error) {
	var m Meta
	info, err := os.Stat(absPath)
	if err != nil {
		return m, err
	}
	m.SizeKB = (info.Size() + 1023) / 1024
	m.Modified = info.ModTime()
	m.Format = strings.TrimPrefix(strings.ToLower(filepath.Ext(absPath)), ".")

	f, err := os.Open(absPath)
	if err != nil {
		return m, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		// RAW isn't decodable by the registered codecs; report the embedded
		// preview's dimensions, which is what we actually display.
		if IsRaw(absPath) {
			if im, derr := DecodeImage(absPath); derr == nil {
				b := im.Bounds()
				m.Width, m.Height = b.Dx(), b.Dy()
			}
		}
		return m, nil // metadata without dimensions is still useful
	}
	m.Width = cfg.Width
	m.Height = cfg.Height
	return m, nil
}
