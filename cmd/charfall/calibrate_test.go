package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/goregular"
)

func TestFontCalibration(t *testing.T) {
	dir := t.TempDir()
	fontFile := filepath.Join(dir, "mono.ttf")
	if err := os.WriteFile(fontFile, gomono.TTF, 0600); err != nil {
		t.Fatal(err)
	}
	var expected glyphProfile
	for _, size := range []float64{6, 10, 16.5, 32} {
		profile, err := measureFont(fontFile, size)
		if err != nil {
			t.Fatalf("Size %g: %v", size, err)
		}
		if profile.FontFamily != "Go Mono" || profile.Raster == nil || profile.Coverage[" "] != 0 || profile.Coverage["@"] <= profile.Coverage["."] {
			t.Fatalf("Invalid calibration: %+v", profile)
		}
		if size == 10 {
			expected = profile
		}
	}
	var output bytes.Buffer
	if err := runCalibration([]string{fontFile}, &output, &output); err != nil {
		t.Fatal(err)
	}
	var decoded glyphProfile
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil || !reflect.DeepEqual(expected, decoded) {
		t.Fatalf("Calibration JSON changed the measurements: %v", err)
	}
	profileFile := filepath.Join(dir, "profile.json")
	if err := runCalibration([]string{"-o", profileFile, fontFile}, &output, &output); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(profileFile)
	if err := runCalibration([]string{fontFile, "-o", profileFile}, &output, &output); err == nil {
		t.Fatal("Calibration overwrote an existing profile")
	}
	if after, _ := os.ReadFile(profileFile); !bytes.Equal(before, after) {
		t.Fatal("Rejected overwrite damaged the profile")
	}
	img := image.NewNRGBA(image.Rect(0, 0, 24, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 24; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: byte(x * 10), G: byte(y * 20), B: byte((x + y) * 6), A: 255})
		}
	}
	opts := options{columns: 24, aspect: .60205, background: "#040711", fontPath: fontFile, fontSize: 10,
		settings: renderSettings{Brightness: 1, Contrast: 1, EdgeStrength: 1, ColorStep: 8, RunTolerance: 10}}
	automatic, err := convert(img, 1, opts)
	if err != nil {
		t.Fatal(err)
	}
	if automatic.CellAspect != expected.FontAdvance {
		t.Fatal("Custom font spacing was not applied automatically")
	}
	opts.fontPath, opts.profilePath = "", profileFile
	saved, err := convert(img, 1, opts)
	if err != nil || !reflect.DeepEqual(automatic, saved) {
		t.Fatalf("Automatic and saved-profile rendering differ: %v", err)
	}
	for _, style := range []string{"classic", "bright", "hybrid", "full-color"} {
		opts.style = style
		ascii, err := convert(img, 1, opts)
		if err != nil {
			t.Fatal(err)
		}
		selected, _ := parseExportFormats(allExportFormats)
		if err := export(ascii, filepath.Join(dir, style), "custom font", false, selected); err != nil {
			t.Fatal(err, style)
		}
		for _, name := range []string{"index.html", "ascii.svg"} {
			data, err := os.ReadFile(filepath.Join(dir, style, name))
			if err != nil || !bytes.Contains(data, []byte("@font-face")) || !bytes.Contains(data, []byte("data:font/ttf;base64,")) {
				t.Fatalf("Custom font is missing from %s: %v", name, err)
			}
		}
	}
	opts.fontPath = fontFile
	if _, _, err := prepareOptions(&opts); err == nil {
		t.Fatal("Accepted both a font file and a profile")
	}
	opts.profilePath, opts.aspectExplicit = "", true
	if _, _, err := prepareOptions(&opts); err == nil {
		t.Fatal("Accepted an explicit aspect incompatible with the measured font")
	}
	for _, size := range []float64{math.NaN(), math.Inf(1), 0, 33} {
		if _, err := measureFont(fontFile, size); err == nil {
			t.Fatal("Accepted invalid font size")
		}
	}
	for _, data := range [][]byte{goregular.TTF, []byte("invalid font")} {
		if err := os.WriteFile(filepath.Join(dir, "bad.ttf"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := measureFont(filepath.Join(dir, "bad.ttf"), 10); err == nil {
			t.Fatal("Accepted a proportional or malformed font")
		}
	}
	large := filepath.Join(dir, "large.ttf")
	file, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate((32 << 20) + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	for _, path := range []string{large, dir, filepath.Join(dir, "missing.ttf")} {
		if _, err := measureFont(path, 10); err == nil {
			t.Fatalf("Accepted invalid font path: %s", path)
		}
	}
	for _, args := range [][]string{nil, {"--size"}, {"--unknown", "1", fontFile}, {fontFile, fontFile}} {
		if err := runCalibration(args, &output, &output); err == nil {
			t.Fatalf("Accepted invalid calibration arguments: %v", args)
		}
	}
}
