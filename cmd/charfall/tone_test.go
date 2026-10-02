package main

import (
	"encoding/json"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestCalibratedRendering(t *testing.T) {
	profile, err := readProfile("", false)
	if err != nil {
		t.Fatal(err)
	}
	image := image.NewNRGBA(image.Rect(0, 0, 256, 1))
	for x := 0; x < 256; x++ {
		image.SetNRGBA(x, 0, color.NRGBA{byte(x), byte(x), byte(x), 255})
	}
	settings := renderSettings{Brightness: 1, Contrast: 0, EdgeStrength: 0, ColorStep: 8, RunTolerance: 0}
	opts := options{columns: 256, aspect: .60205, background: "#040711", quality: "tone", settings: settings}
	current, err := convert(image, 1, opts)
	if err != nil {
		t.Fatal(err)
	}
	peak := profile.Coverage["@"]
	bg, _ := parseBackground(opts.background)
	backdrop := luma([3]float64{float64(bg[0]) / 255, float64(bg[1]) / 255, float64(bg[2]) / 255})
	errorFor := func(ascii asciiOutput) float64 {
		x, total := 0, 0.0
		for _, r := range ascii.Lines[0] {
			ink, _ := parseBackground(r[0])
			inkLuma := luma([3]float64{float64(ink[0]) / 255, float64(ink[1]) / 255, float64(ink[2]) / 255})
			for _, glyph := range r[1] {
				coverage := profile.Coverage[string(glyph)]
				predicted := backdrop*(1-coverage) + coverage*inkLuma
				cell := [5]float32{0, 0, float32(x) / 255, float32(x) / 255, float32(x) / 255}
				target := backdrop + (1-backdrop)*peak*luma(sourceInk(cell))
				total += math.Abs(predicted - target)
				x++
			}
		}
		if x != 256 {
			t.Fatalf("Wrong grid width: %d", x)
		}
		return total / 256
	}
	calibratedError := errorFor(current)
	if calibratedError >= .003 {
		t.Fatalf("Measured-font gradient brightness error exceeds tolerance: %g", calibratedError)
	}
	t.Logf("Measured-font gradient brightness error: %.6f", calibratedError)
	if current.Rendering != "calibrated-joint" || current.Profile == nil || current.DirectionalFraction != 0 {
		t.Fatal("Missing calibration metadata or edge-disable control")
	}
	sumBrightness := func(ascii asciiOutput) float64 {
		sum := 0.0
		for _, r := range ascii.Lines[0] {
			ink, _ := parseBackground(r[0])
			for _, g := range r[1] {
				sum += profile.Coverage[string(g)] * luma([3]float64{float64(ink[0]) / 255, float64(ink[1]) / 255, float64(ink[2]) / 255})
			}
		}
		return sum
	}
	opts.settings.Brightness = .6
	dim, err := convert(image, 1, opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.settings.Brightness = 1.4
	bright, err := convert(image, 1, opts)
	if err != nil {
		t.Fatal(err)
	}
	if sumBrightness(bright) <= sumBrightness(dim) {
		t.Fatal("Brightness control does not increase rendered brightness")
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), 0, 4} {
		bad := settings
		bad.Brightness = value
		if validateSettings(bad) == nil {
			t.Fatal("Invalid brightness accepted")
		}
	}
}

func TestShapeMatching(t *testing.T) {
	profile, err := readProfile("", true)
	if err != nil {
		t.Fatal(err)
	}
	settings := renderSettings{Brightness: 1, Contrast: 1, EdgeStrength: 0, ColorStep: 8, RunTolerance: 0}
	cell := [5]float32{.4, .16, .4, .4, .4}
	bg := [3]byte{4, 7, 17}
	glyph, ink, _ := fitCell(cell, .4, 0, 0, profile, bg, settings, nil)
	flat := make([]float64, 60)
	for i := range flat {
		flat[i] = .4
	}
	flatGlyph, flatInk, _ := fitCell(cell, .4, 0, 0, profile, bg, settings, flat)
	if glyph != flatGlyph || ink != flatInk {
		t.Fatal("Flat patches must retain the tone-only fit")
	}

	profile.Coverage = map[string]float64{}
	profile.Masks = map[string][]float64{}
	left, right := []float64{1, 0, 1, 0}, []float64{0, 1, 0, 1}
	for _, g := range " .:cCoO8@/\\_|-" {
		profile.Coverage[string(g)] = .5
		profile.Masks[string(g)] = left
	}
	profile.Coverage[" "] = 0
	profile.Masks["C"] = right
	matched, _, _ := fitCell(cell, .4, 0, 0, profile, bg, settings, right)
	if matched != 'C' {
		t.Fatalf("Stroke matching ignored source geometry: got %c", matched)
	}

	profile, _ = readProfile("", true)
	file := filepath.Join(t.TempDir(), "bad-profile.json")
	profile.Masks["c"][0] = 2
	data, _ := json.Marshal(profile)
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readProfile(file, true); err == nil {
		t.Fatal("Invalid mask alpha accepted")
	}
	if _, err = readProfile("profiles/menlo-profile.json", true); err == nil {
		t.Fatal("Coverage-only profile accepted for shape matching")
	}
}

func TestAutomaticTone(t *testing.T) {
	settings := renderSettings{Brightness: 1, Contrast: 1, EdgeStrength: 1, ColorStep: 8, RunTolerance: 10}
	improved := false
	for _, scene := range []struct {
		name   string
		lo, hi byte
	}{
		{"night", 2, 40}, {"bright", 180, 255}, {"full-range", 0, 255},
		{"black", 0, 0}, {"white", 255, 255}, {"near-flat", 100, 104},
	} {
		t.Run(scene.name, func(t *testing.T) {
			img := image.NewNRGBA(image.Rect(0, 0, 256, 16))
			for y := 0; y < 16; y++ {
				for x := 0; x < 256; x++ {
					v := byte(float64(scene.lo) + float64(scene.hi-scene.lo)*float64(x)/255)
					img.SetNRGBA(x, y, color.NRGBA{v, v, v, 255})
				}
			}
			opts := options{columns: 256, aspect: .60205, background: "#040711", quality: "tone", settings: settings}
			before, err := convert(img, 1, opts)
			if err != nil {
				t.Fatal(err)
			}
			opts.autoTone = true
			after, err := convert(img, 1, opts)
			if err != nil {
				t.Fatal(err)
			}
			a := after.ToneAnalysis
			if scene.name == "full-range" && (math.Abs(a.Source.Mean-.5) > .003 || math.Abs(a.Source.DynamicRange-.9) > .02) {
				t.Fatalf("Incorrect brightness distribution: %+v", a.Source)
			}
			if !a.Automatic || !safeTone(before.ToneAnalysis.Rendered, a.Rendered, a.Source) {
				t.Fatalf("Unsafe automatic adjustment: %+v", a)
			}
			if after.Settings.AutoLift < 0 || after.Settings.AutoLift > .09 || after.Settings.Contrast < .9 || after.Settings.Contrast > 1.1 {
				t.Fatalf("Unbounded settings: %+v", after.Settings)
			}
			if scene.hi-scene.lo <= 4 && (a.Adjusted || after.Settings != nil && *after.Settings != settings) {
				t.Fatal("Flat image was normalized or brightened")
			}
			if a.Adjusted && a.Rendered.SourceMAE < before.ToneAnalysis.Rendered.SourceMAE-.001 {
				improved = true
			}
			again, _ := convert(img, 1, opts)
			one, _ := json.Marshal(after)
			two, _ := json.Marshal(again)
			if string(one) != string(two) {
				t.Fatal("Automatic rendering is not deterministic")
			}
			t.Logf("lift %.2f, contrast %.2f, source error %.5f -> %.5f, mean %.5f -> %.5f", after.Settings.AutoLift, after.Settings.Contrast, before.ToneAnalysis.Rendered.SourceMAE, a.Rendered.SourceMAE, before.ToneAnalysis.Rendered.Mean, a.Rendered.Mean)
		})
	}
	if !improved {
		t.Fatal("Automatic adjustment did not improve any non-flat ramp")
	}

	var good, collapsed toneAccumulator
	for i := 1; i < 99; i++ {
		source := float64(i) / 100
		good.add(source, source*.5, .5)
		collapsed.add(source, .35, .5)
	}
	distribution := sourceTones{Mean: .5, Percentiles: [5]float64{.01, .05, .5, .95, .99}}
	if safeTone(good.measure(), collapsed.measure(), distribution) {
		t.Fatal("Accepted collapsed tonal detail")
	}
	before := good.measure()
	clipped := before
	clipped.HighlightClippedFraction = .2
	if safeTone(before, clipped, distribution) {
		t.Fatal("Accepted new highlight clipping")
	}
}
