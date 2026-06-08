package img

// Camera RAW support without libraw. ARW (and most other RAW formats) are
// TIFF containers that embed a full-resolution JPEG preview alongside the
// sensor data. Rather than link a native decoder — which would break the
// FROM-scratch container the same way HEIC would — we parse the TIFF
// directory structure and pull out the largest embedded JPEG. That preview
// is all a culling tool needs to display, thumbnail, hash, and inspect.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// rawExts lists the RAW extensions handled via embedded-preview extraction.
var rawExts = map[string]bool{
	".arw": true, // Sony
}

// IsRaw reports whether path carries a supported RAW extension.
func IsRaw(path string) bool {
	return rawExts[strings.ToLower(filepath.Ext(path))]
}

// DecodeImage decodes path into an image. RAW files are decoded from their
// embedded JPEG preview; everything else goes through the codecs registered
// in this package (stdlib JPEG/PNG/GIF + x/image WebP).
func DecodeImage(path string) (image.Image, error) {
	if IsRaw(path) {
		jpg, err := ExtractPreview(path)
		if err != nil {
			return nil, err
		}
		return jpeg.Decode(bytes.NewReader(jpg))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	im, _, err := image.Decode(f)
	return im, err
}

// ExtractPreview returns the largest embedded JPEG preview from a TIFF-based
// RAW file. It walks the IFD chain plus any SubIFDs, collecting every JPEG
// referenced by the JPEGInterchangeFormat/Length tag pair (0x0201/0x0202) or
// by strip offsets on a JPEG-compressed directory, then returns the biggest
// candidate that actually begins with an SOI marker.
func ExtractPreview(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var hdr [8]byte
	if _, err := f.ReadAt(hdr[:], 0); err != nil {
		return nil, err
	}
	var bo binary.ByteOrder
	switch {
	case hdr[0] == 'I' && hdr[1] == 'I':
		bo = binary.LittleEndian
	case hdr[0] == 'M' && hdr[1] == 'M':
		bo = binary.BigEndian
	default:
		return nil, errors.New("not a TIFF-based RAW")
	}
	if bo.Uint16(hdr[2:4]) != 0x002A {
		return nil, errors.New("bad TIFF magic")
	}

	type region struct{ off, length uint32 }
	var jpegs []region
	visited := map[uint32]bool{}
	queue := []uint32{bo.Uint32(hdr[4:8])}

	// Bounded BFS over the IFD tree to tolerate malformed/looping files.
	for len(queue) > 0 && len(visited) < 64 {
		off := queue[0]
		queue = queue[1:]
		if off == 0 || visited[off] {
			continue
		}
		visited[off] = true

		var cntBuf [2]byte
		if _, err := f.ReadAt(cntBuf[:], int64(off)); err != nil {
			continue
		}
		n := int(bo.Uint16(cntBuf[:]))
		if n == 0 || n > 4096 {
			continue
		}
		entries := make([]byte, n*12+4) // entries followed by next-IFD pointer
		rd, _ := f.ReadAt(entries, int64(off)+2)
		if rd < n*12 {
			continue // directory truncated; skip it
		}

		var jpegOff, jpegLen, stripOff, stripLen uint32
		var compression uint16
		for i := 0; i < n; i++ {
			e := entries[i*12 : i*12+12]
			tag := bo.Uint16(e[0:2])
			ftype := bo.Uint16(e[2:4])
			count := bo.Uint32(e[4:8])
			val := e[8:12]
			scalar := func() uint32 {
				if ftype == 3 { // SHORT, left-justified in the value field
					return uint32(bo.Uint16(val[:2]))
				}
				return bo.Uint32(val) // LONG
			}
			switch tag {
			case 0x0103: // Compression
				compression = bo.Uint16(val[:2])
			case 0x0111: // StripOffsets
				if count == 1 {
					stripOff = scalar()
				}
			case 0x0117: // StripByteCounts
				if count == 1 {
					stripLen = scalar()
				}
			case 0x0201: // JPEGInterchangeFormat (preview offset)
				jpegOff = scalar()
			case 0x0202: // JPEGInterchangeFormatLength
				jpegLen = scalar()
			case 0x014A: // SubIFDs — one or more LONG offsets
				queue = append(queue, readOffsets(f, bo, count, val)...)
			}
		}
		if jpegOff > 0 && jpegLen > 0 {
			jpegs = append(jpegs, region{jpegOff, jpegLen})
		}
		if compression == 6 && stripOff > 0 && stripLen > 0 {
			jpegs = append(jpegs, region{stripOff, stripLen})
		}
		// Follow the next-IFD pointer (IFD1 holds the thumbnail/preview).
		if rd >= n*12+4 {
			queue = append(queue, bo.Uint32(entries[n*12:n*12+4]))
		}
	}

	// Largest first; return the first candidate that reads back as a JPEG.
	sort.Slice(jpegs, func(i, j int) bool { return jpegs[i].length > jpegs[j].length })
	for _, r := range jpegs {
		if r.length < 2 || r.length > 100<<20 {
			continue
		}
		buf := make([]byte, r.length)
		if _, err := f.ReadAt(buf, int64(r.off)); err != nil {
			continue
		}
		if buf[0] == 0xFF && buf[1] == 0xD8 {
			return buf, nil
		}
	}
	return nil, errors.New("no embedded JPEG preview found")
}

// readOffsets resolves a TIFF value field holding count LONG offsets. When
// count is 1 the offset sits inline; otherwise the field points at an array.
func readOffsets(f *os.File, bo binary.ByteOrder, count uint32, val []byte) []uint32 {
	if count == 0 || count > 16 {
		return nil
	}
	if count == 1 {
		return []uint32{bo.Uint32(val)}
	}
	buf := make([]byte, count*4)
	if _, err := f.ReadAt(buf, int64(bo.Uint32(val))); err != nil {
		return nil
	}
	out := make([]uint32, count)
	for i := range out {
		out[i] = bo.Uint32(buf[i*4:])
	}
	return out
}
