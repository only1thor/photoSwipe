package img

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestIsRaw(t *testing.T) {
	cases := map[string]bool{
		"/photos/a.ARW": true,
		"b.arw":         true,
		"c.jpg":         false,
		"d.tiff":        false,
		"no-extension":  false,
	}
	for path, want := range cases {
		if got := IsRaw(path); got != want {
			t.Errorf("IsRaw(%q) = %v, want %v", path, got, want)
		}
	}
}

// buildTIFF assembles a little-endian TIFF whose IFD0 references jpegBig via
// the JPEGInterchangeFormat tag pair and chains to IFD1 referencing jpegSmall.
// ExtractPreview should return the larger of the two.
func buildTIFF(jpegBig, jpegSmall []byte) []byte {
	const ifd0 = 8
	const ifdSize = 2 + 2*12 + 4 // count + 2 entries + next-IFD pointer
	const ifd1 = ifd0 + ifdSize
	bigOff := ifd1 + ifdSize
	smallOff := bigOff + len(jpegBig)

	buf := make([]byte, smallOff+len(jpegSmall))
	le := binary.LittleEndian
	copy(buf[0:2], "II")
	le.PutUint16(buf[2:], 0x002A)
	le.PutUint32(buf[4:], ifd0)

	writeIFD := func(at int, jpegOff, jpegLen uint32, next uint32) {
		le.PutUint16(buf[at:], 2) // entry count
		e := at + 2
		// 0x0201 JPEGInterchangeFormat (LONG)
		le.PutUint16(buf[e:], 0x0201)
		le.PutUint16(buf[e+2:], 4)
		le.PutUint32(buf[e+4:], 1)
		le.PutUint32(buf[e+8:], jpegOff)
		// 0x0202 JPEGInterchangeFormatLength (LONG)
		le.PutUint16(buf[e+12:], 0x0202)
		le.PutUint16(buf[e+14:], 4)
		le.PutUint32(buf[e+16:], 1)
		le.PutUint32(buf[e+20:], jpegLen)
		le.PutUint32(buf[at+2+24:], next)
	}
	writeIFD(ifd0, uint32(bigOff), uint32(len(jpegBig)), ifd1)
	writeIFD(ifd1, uint32(smallOff), uint32(len(jpegSmall)), 0)

	copy(buf[bigOff:], jpegBig)
	copy(buf[smallOff:], jpegSmall)
	return buf
}

func TestExtractPreview_PicksLargestJPEG(t *testing.T) {
	jpegSmall := append([]byte{0xFF, 0xD8}, append(bytes.Repeat([]byte{0x11}, 64), 0xFF, 0xD9)...)
	jpegBig := append([]byte{0xFF, 0xD8}, append(bytes.Repeat([]byte{0x22}, 4096), 0xFF, 0xD9)...)

	dir := t.TempDir()
	path := filepath.Join(dir, "sample.arw")
	if err := os.WriteFile(path, buildTIFF(jpegBig, jpegSmall), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ExtractPreview(path)
	if err != nil {
		t.Fatalf("ExtractPreview: %v", err)
	}
	if !bytes.Equal(got, jpegBig) {
		t.Fatalf("expected the larger embedded JPEG (%d bytes), got %d", len(jpegBig), len(got))
	}
}

func TestExtractPreview_RejectsNonTIFF(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "not.arw")
	if err := os.WriteFile(path, []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0}, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractPreview(path); err == nil {
		t.Fatal("expected error for non-TIFF input")
	}
}
