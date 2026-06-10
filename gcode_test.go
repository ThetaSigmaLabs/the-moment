package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"strings"
	"testing"
)

// makePNG creates a w×h RGBA PNG with random pixels. Random data resists deflate
// compression so the output is reliably large — useful for testing the ≥5KB guard.
func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewSource(42))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(r.Intn(256)),
				G: uint8(r.Intn(256)),
				B: uint8(r.Intn(256)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("makePNG: encode failed: %v", err)
	}
	return buf.Bytes()
}

// bgcode wraps content with the GCDE magic header so ParseGcodeMetadata treats
// it as a binary gcode file and runs the bgcode thumbnail extraction path.
func wrapBgcode(content []byte) []byte {
	out := make([]byte, 0, 4+len(content))
	out = append(out, 'G', 'C', 'D', 'E')
	return append(out, content...)
}

func TestParseGcodeMetadata_OrcaTime(t *testing.T) {
	gcode := []byte(";TIME:1234\nG28\n")
	sec, thumb := ParseGcodeMetadata(gcode)
	if sec != 1234 {
		t.Errorf("expected 1234 seconds, got %d", sec)
	}
	if thumb != "" {
		t.Errorf("expected no thumbnail, got non-empty string")
	}
}

func TestParseGcodeMetadata_PrusaTimeNodays(t *testing.T) {
	gcode := []byte("; estimated printing time (normal mode) = 2h 15m 30s\n")
	sec, _ := ParseGcodeMetadata(gcode)
	want := 2*3600 + 15*60 + 30
	if sec != want {
		t.Errorf("expected %d seconds, got %d", want, sec)
	}
}

func TestParseGcodeMetadata_PrusaTimeWithDays(t *testing.T) {
	// Confirms the days component regex fix — this format broke prior to the fix.
	gcode := []byte("estimated printing time (normal mode) = 1d 6h 37m 3s\n")
	sec, _ := ParseGcodeMetadata(gcode)
	want := 1*86400 + 6*3600 + 37*60 + 3
	if sec != want {
		t.Errorf("expected %d seconds (1d 6h 37m 3s), got %d", want, sec)
	}
}

func TestParseGcodeMetadata_AsciiThumbnailJPG(t *testing.T) {
	b64 := "AAABBBCCC"
	gcode := []byte("; thumbnail_JPG begin 16x16 9\n; " + b64 + "\n; thumbnail_JPG end\n")
	_, thumb := ParseGcodeMetadata(gcode)
	want := "data:image/jpeg;base64," + b64
	if thumb != want {
		t.Errorf("expected %q, got %q", want, thumb)
	}
}

func TestParseGcodeMetadata_BgcodeThumbnail(t *testing.T) {
	// 100×100 random-pixel PNG produces a file well over 5 KB (the minimum size
	// threshold that parseBgcodeThumbnail uses to skip printer-display icons).
	pngData := makePNG(t, 100, 100)
	if len(pngData) < 5000 {
		t.Skipf("generated PNG only %d bytes — need ≥5000 for bgcode thumbnail test", len(pngData))
	}
	bgcode := wrapBgcode(pngData)
	_, thumb := ParseGcodeMetadata(bgcode)
	if !strings.HasPrefix(thumb, "data:image/png;base64,") {
		t.Errorf("expected PNG data URI, got %q", thumb)
	}
}

func TestParseGcodeMetadata_BgcodeTinyThumbnailSkipped(t *testing.T) {
	// 8×8 PNG is a printer-display icon (well under 5 KB) and should be skipped.
	pngData := makePNG(t, 8, 8)
	if len(pngData) >= 5000 {
		t.Skipf("generated PNG unexpectedly large (%d bytes) — expected <5000", len(pngData))
	}
	bgcode := wrapBgcode(pngData)
	_, thumb := ParseGcodeMetadata(bgcode)
	if thumb != "" {
		t.Errorf("expected empty thumbnail for tiny bgcode icon, got non-empty string")
	}
}

func TestParseGcodeMetadata_Empty(t *testing.T) {
	sec, thumb := ParseGcodeMetadata(nil)
	if sec != 0 || thumb != "" {
		t.Errorf("nil input: expected (0, \"\"), got (%d, %q)", sec, thumb)
	}
	sec, thumb = ParseGcodeMetadata([]byte{})
	if sec != 0 || thumb != "" {
		t.Errorf("empty input: expected (0, \"\"), got (%d, %q)", sec, thumb)
	}
}

func TestParseGcodeMetadata_OrcaTimeTakesPrecedence(t *testing.T) {
	// When both ;TIME: and a PrusaSlicer comment are present, ;TIME: wins.
	gcode := []byte(";TIME:500\n; estimated printing time (normal mode) = 2h 0m 0s\n")
	sec, _ := ParseGcodeMetadata(gcode)
	if sec != 500 {
		t.Errorf("expected ;TIME: value 500, got %d", sec)
	}
}
