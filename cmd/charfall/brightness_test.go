package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"testing"
)

func TestBrightnessStyles(t *testing.T) {
	profile, err := readProfile("", true)
	if err != nil {
		t.Fatal(err)
	}
	settings := renderSettings{Brightness: 1, Contrast: 1, EdgeStrength: 1, ColorStep: 1, RunTolerance: 0}
	bg := [3]byte{4, 7, 17}
	_, ceiling := toneBackdrop(profile, bg)
	white := [5]float32{1, 1, 1, 1, 1}
	settings.Style = "bright"
	bright := fitCandidates(white, .9, 0, 0, profile, bg, settings, nil, nil)
	if bright.backgroundSet || bright.coverage < profile.Coverage["@"] {
		t.Fatal("Bright style failed to use maximum coverage on white")
	}
	settings.Style = "hybrid"
	hybrid := fitCandidates(white, .9, 0, 0, profile, bg, settings, nil, nil)
	if !hybrid.backgroundSet || hybrid.background[0] > int((float64(bg[0])+(255-float64(bg[0]))*hybridLimit)+1) {
		t.Fatal("Hybrid assistance missing or not bounded")
	}
	dark := [5]float32{.15, .0225, .15, .15, .15}
	if fitCandidates(dark, .3, 0, 0, profile, bg, settings, nil, nil).backgroundSet {
		t.Fatal("Reachable dark cell received hybrid background")
	}
	settings.Style = "full-color"
	full := fitCandidates(white, .9, 0, 0, profile, bg, settings, nil, nil)
	if !full.backgroundSet || full.error*full.error > 1e-6 {
		t.Fatal("Full-color did not recover white")
	}
	if ceiling >= .75 {
		t.Fatal("Fixture does not expose the foreground ceiling")
	}

	mask := profile.Masks["/"]
	patch := make([]float64, len(mask)*4)
	mean := 0.0
	for i, a := range mask {
		v := .1 + .8*a
		mean += v / float64(len(mask))
		patch[i] = v
		for c := 0; c < 3; c++ {
			patch[len(mask)+i*3+c] = v
		}
	}
	f, b := fittedPair(mask, patch, [3]float64{mean, mean, mean})
	if f[0] < .899 || f[0] > .901 || b[0] < .099 || b[0] > .101 {
		t.Fatalf("Measured mask regression failed: %v %v", f, b)
	}

	img := image.NewNRGBA(image.Rect(0, 0, 32, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 32; x++ {
			v := byte(x * 255 / 31)
			img.SetNRGBA(x, y, color.NRGBA{v, v, v, 255})
		}
	}
	settings.Style = ""
	opts := options{columns: 32, aspect: .60205, background: "#040711", settings: settings}
	def, err := convert(img, 1, opts)
	if err != nil || def.Rendering != "calibrated-neighborhood" {
		t.Fatalf("Neighborhood is not the entrypoint default: %v", err)
	}
	for _, mode := range []string{"tone", "shape", "neighborhood"} {
		opts.quality = mode
		for _, style := range []string{"classic", "bright", "hybrid", "full-color"} {
			opts.style = style
			a, err := convert(img, 1, opts)
			if err != nil {
				t.Fatal(err)
			}
			again, err := convert(img, 1, opts)
			if err != nil {
				t.Fatal(err)
			}
			one, _ := json.Marshal(a)
			two, _ := json.Marshal(again)
			if !bytes.Equal(one, two) {
				t.Fatal("Nondeterministic brightness style", style)
			}
			for _, line := range a.Lines {
				for _, r := range line {
					if style == "classic" && len(r) != 2 {
						t.Fatal("Classic schema changed")
					}
				}
			}
			exports, err := extraFormats(a, style, map[string]bool{"ansi": true, "svg": true, "png": true})
			if err != nil {
				t.Fatal(err)
			}
			if len(exports) != 3 {
				t.Fatal("Missing formats")
			}
			if style == "hybrid" || style == "full-color" {
				if !bytes.Contains(exports[1], []byte(`<rect x=`)) {
					t.Fatal("SVG omitted cell backgrounds")
				}
				if a.ToneAnalysis.Rendered.BackgroundFraction == 0 {
					t.Fatal("Background diagnostics omitted")
				}
			}
		}
	}
}
