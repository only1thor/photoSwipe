package img

// Minimal JPEG/EXIF reader. Surfaces just the two tags photoSwipe consumes:
// Orientation (so rotated phone shots hash and display correctly) and
// DateTimeOriginal (so the duplicate-clustering time window is meaningful
// even after a file has been re-saved).
//
// Hand-rolled rather than pulling in an EXIF library — keeps the dep count at
// one. Only JPEG is parsed; PNG/GIF/WebP return zero values silently.

import (
	"encoding/binary"
	"errors"
	"image"
	"io"
	"os"
	"time"
)

// ExifInfo is the EXIF subset we care about.
type ExifInfo struct {
	Orientation      int       // 1..8; 1 (or 0) means "no rotation"
	DateTimeOriginal time.Time // zero if absent
}

// ReadExifFile parses path as a JPEG and returns its EXIF subset. Any error
// is swallowed and reported as a zero ExifInfo — callers should treat "no
// EXIF" and "EXIF parse failed" identically.
func ReadExifFile(path string) ExifInfo {
	f, err := os.Open(path)
	if err != nil {
		return ExifInfo{}
	}
	defer f.Close()
	info, err := readExif(f)
	if err != nil {
		return ExifInfo{}
	}
	return info
}

func readExif(r io.Reader) (ExifInfo, error) {
	var soi [2]byte
	if _, err := io.ReadFull(r, soi[:]); err != nil {
		return ExifInfo{}, err
	}
	if soi[0] != 0xFF || soi[1] != 0xD8 {
		return ExifInfo{}, errors.New("not a jpeg")
	}
	for {
		var m [2]byte
		if _, err := io.ReadFull(r, m[:]); err != nil {
			return ExifInfo{}, err
		}
		if m[0] != 0xFF {
			return ExifInfo{}, errors.New("bad marker")
		}
		marker := m[1]
		// Standalone markers with no length payload.
		if marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7) {
			continue
		}
		// Start-of-scan or end-of-image — EXIF won't appear later.
		if marker == 0xDA || marker == 0xD9 {
			return ExifInfo{}, errors.New("no exif")
		}
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return ExifInfo{}, err
		}
		segLen := int(binary.BigEndian.Uint16(lenBuf[:])) - 2
		if segLen < 0 || segLen > 1<<20 {
			return ExifInfo{}, errors.New("bad segment length")
		}
		seg := make([]byte, segLen)
		if _, err := io.ReadFull(r, seg); err != nil {
			return ExifInfo{}, err
		}
		if marker == 0xE1 && len(seg) >= 6 && string(seg[:6]) == "Exif\x00\x00" {
			return parseExif(seg[6:])
		}
	}
}

func parseExif(tiff []byte) (ExifInfo, error) {
	var info ExifInfo
	if len(tiff) < 8 {
		return info, errors.New("tiff header truncated")
	}
	var bo binary.ByteOrder
	switch {
	case tiff[0] == 'I' && tiff[1] == 'I':
		bo = binary.LittleEndian
	case tiff[0] == 'M' && tiff[1] == 'M':
		bo = binary.BigEndian
	default:
		return info, errors.New("bad byte order")
	}
	if bo.Uint16(tiff[2:4]) != 0x002A {
		return info, errors.New("bad tiff magic")
	}
	ifd0 := int(bo.Uint32(tiff[4:8]))
	exifSubIFD := walkIFD(tiff, ifd0, bo, &info)
	if exifSubIFD > 0 {
		walkIFD(tiff, exifSubIFD, bo, &info)
	}
	return info, nil
}

// walkIFD walks one Image File Directory and updates info in place. Returns
// the Exif sub-IFD offset (tag 0x8769) if encountered, else 0.
func walkIFD(tiff []byte, off int, bo binary.ByteOrder, info *ExifInfo) int {
	if off < 0 || off+2 > len(tiff) {
		return 0
	}
	n := int(bo.Uint16(tiff[off : off+2]))
	base := off + 2
	if base+n*12 > len(tiff) {
		return 0
	}
	var subIFD int
	for i := 0; i < n; i++ {
		e := base + i*12
		tag := bo.Uint16(tiff[e : e+2])
		ftype := bo.Uint16(tiff[e+2 : e+4])
		count := bo.Uint32(tiff[e+4 : e+8])
		val := tiff[e+8 : e+12]
		switch tag {
		case 0x0112: // Orientation, SHORT count=1
			if ftype == 3 && count == 1 {
				info.Orientation = int(bo.Uint16(val[:2]))
			}
		case 0x8769: // Exif IFD pointer, LONG count=1
			if ftype == 4 && count == 1 {
				subIFD = int(bo.Uint32(val))
			}
		case 0x9003: // DateTimeOriginal, ASCII "YYYY:MM:DD HH:MM:SS\0"
			if ftype == 2 && count >= 19 {
				var s string
				if count <= 4 {
					s = string(val[:count])
				} else {
					strOff := int(bo.Uint32(val))
					end := strOff + int(count)
					if strOff >= 0 && end <= len(tiff) {
						s = string(tiff[strOff:end])
					}
				}
				// Trim trailing nulls / spaces.
				for len(s) > 0 && (s[len(s)-1] == 0 || s[len(s)-1] == ' ') {
					s = s[:len(s)-1]
				}
				if t, err := time.Parse("2006:01:02 15:04:05", s); err == nil {
					info.DateTimeOriginal = t
				}
			}
		}
	}
	return subIFD
}

// ApplyOrientation returns src rotated/flipped into display orientation
// according to an EXIF Orientation tag (1..8). Values outside that range
// (including the common 0 = "tag absent") return src unchanged.
//
// Cost is O(width × height) per call. Only invoked once per photo, on
// hashing or first thumbnail generation, so the simple per-pixel copy is
// fine — perf is dominated by JPEG decode upstream.
func ApplyOrientation(src image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return src
	}
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dw, dh := sw, sh
	if orientation >= 5 {
		dw, dh = sh, sw
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < sh; y++ {
		for x := 0; x < sw; x++ {
			var nx, ny int
			switch orientation {
			case 2:
				nx, ny = sw-1-x, y
			case 3:
				nx, ny = sw-1-x, sh-1-y
			case 4:
				nx, ny = x, sh-1-y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = sh-1-y, x
			case 7:
				nx, ny = sh-1-y, sw-1-x
			case 8:
				nx, ny = y, sw-1-x
			}
			dst.Set(nx, ny, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
