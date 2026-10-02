package main

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestRunGrouping(t *testing.T) {
	selected := []assignment{
		{glyph: 'c', rgb: [3]int{10, 20, 30}},
		{glyph: 'C', rgb: [3]int{20, 30, 40}},
		{glyph: '@', rgb: [3]int{21, 31, 41}},
		{glyph: '/', rgb: [3]int{21, 31, 41}, backgroundSet: true, background: [3]int{1, 2, 3}},
		{glyph: '\\', rgb: [3]int{21, 31, 41}, backgroundSet: true, background: [3]int{11, 12, 13}},
		{glyph: ' ', rgb: [3]int{21, 31, 41}, backgroundSet: true, background: [3]int{12, 13, 14}},
	}
	ascii := asciiOutput{metadata: metadata{Columns: 3, Rows: 2}}
	assembleASCII(&ascii, 10, func(i int) *assignment { return &selected[i] })
	want := [][]run{
		{{"#0a141e", "cC"}, {"#151f29", "@"}},
		{{"#151f29", "/\\", "#010203"}, {"#151f29", " ", "#0c0d0e"}},
	}
	if !reflect.DeepEqual(ascii.Lines, want) || ascii.Runs != 4 || ascii.Colors != 2 {
		t.Fatalf("Run boundaries, colors, or row text changed: %+v", ascii)
	}
	long := make([]assignment, 20_000)
	for i := range long {
		long[i] = assignment{glyph: '@', rgb: [3]int{255, 255, 255}}
	}
	ascii = asciiOutput{metadata: metadata{Columns: len(long), Rows: 1}}
	assembleASCII(&ascii, 10, func(i int) *assignment { return &long[i] })
	if ascii.Runs != 1 || ascii.Lines[0][0][1] != strings.Repeat("@", len(long)) {
		t.Fatal("Long run text was truncated")
	}
}

func BenchmarkAssembleLongRun(b *testing.B) {
	selected := make([]assignment, 20_000)
	for i := range selected {
		selected[i] = assignment{glyph: '@', rgb: [3]int{255, 255, 255}}
	}
	ascii := asciiOutput{metadata: metadata{Columns: len(selected), Rows: 1}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		assembleASCII(&ascii, 10, func(i int) *assignment { return &selected[i] })
	}
}

func TestSpatialRefinement(t *testing.T) {
	profile, err := readProfile("", true)
	if err != nil {
		t.Fatal(err)
	}
	red := oklab([3]float64{1, 0, 0})
	if math.Abs(red[0]-.62795536) > 1e-6 || math.Abs(red[1]-.22486306) > 1e-6 || math.Abs(red[2]-.12584630) > 1e-6 {
		t.Fatal("Oklab transform does not match the published sRGB reference")
	}
	settings := renderSettings{Brightness: 1, Contrast: 1, EdgeStrength: 1, ColorStep: 8, RunTolerance: 10}
	lists := make([][4]assignment, 24)
	features := make([]sourceFeature, 24)
	for i := range lists {
		glyph, other := byte('c'), byte('C')
		if i%2 == 1 {
			glyph, other = other, glyph
		}
		for j := range lists[i] {
			lists[i][j].score = math.Inf(1)
		}
		lists[i][0] = assignment{glyph: glyph, rgb: [3]int{96, 112, 160}, coverage: profile.Coverage[string(glyph)]}
		lists[i][1] = assignment{glyph: other, rgb: [3]int{96, 112, 160}, coverage: profile.Coverage[string(other)], score: .00001}
		features[i] = sourceFeature{lab: oklab([3]float64{.1, .15, .3}), luma: .15}
	}
	refined, diagnostics := refine(lists, features, 24, 1, profile, [3]byte{4, 7, 17}, settings, spatialSettings{Strength: 1, Complexity: "balanced"})
	transitions := 0
	for i := 1; i < len(refined); i++ {
		if refined[i].glyph != refined[i-1].glyph {
			transitions++
		}
	}
	if transitions >= 12 || diagnostics.GlyphChanges == 0 || diagnostics.Passes != 2 {
		t.Fatal("Flat-region chatter was not reduced")
	}
	for _, c := range refined {
		if c.score > .0015 {
			t.Fatal("Refinement exceeded its reconstruction budget")
		}
	}

	features[0] = sourceFeature{lab: oklab([3]float64{1, 0, 0}), luma: .2126}
	features[1] = sourceFeature{lab: oklab([3]float64{0, 0, 1}), luma: .0722}
	boundary, _ := refine(lists[:2], features[:2], 2, 1, profile, [3]byte{}, settings, spatialSettings{Strength: 3, Complexity: "compact"})
	if boundary[0].glyph != lists[0][0].glyph || boundary[1].glyph != lists[1][0].glyph {
		t.Fatal("Red/blue boundary was smoothed")
	}
	for i := range features {
		features[i].contrast = .25
		features[i].gradient = .3
	}
	detail, _ := refine(lists, features, 24, 1, profile, [3]byte{}, settings, spatialSettings{Strength: 3, Complexity: "compact"})
	for i := range detail {
		if detail[i].glyph != lists[i][0].glyph {
			t.Fatal("High-contrast texture was smoothed")
		}
	}
	edge := sourceFeature{lab: red, gradient: .2, gx: -.15, gy: .15, edge: '/'}
	if edgeLink(edge, edge, 1, 1) == 0 || edgeLink(edge, edge, 1, -1) != 0 {
		t.Fatal("Edge link ignored source tangent")
	}
	strokes := strokeDistances(profile)
	if strokes['/']['/'] != 0 || strokes['/']['\\'] <= 0 {
		t.Fatal("Stroke geometry compatibility is incorrect")
	}

	source := image.NewNRGBA(image.Rect(0, 0, 80, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 80; x++ {
			source.SetNRGBA(x, y, color.NRGBA{byte(x * 3), byte(y * 6), byte((x + y) * 2), 255})
		}
	}
	opts := options{columns: 40, aspect: .60205, background: "#040711", quality: "shape", settings: settings}
	shape, err := convert(source, 1, opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.quality = "neighborhood"
	opts.spatial = spatialSettings{Strength: 0, Complexity: "balanced"}
	zero, err := convert(source, 1, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(shape.Lines, zero.Lines) {
		t.Fatal("Zero coherence did not preserve shape cells")
	}
	opts.spatial.Strength = 1
	a, err := convert(source, 1, opts)
	if err != nil {
		t.Fatal(err)
	}
	b, err := convert(source, 1, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("Neighborhood conversion is nondeterministic")
	}
	one, err := extraFormats(a, "test <title>", map[string]bool{"ansi": true, "svg": true, "png": true})
	if err != nil {
		t.Fatal(err)
	}
	two, err := extraFormats(b, "test <title>", map[string]bool{"ansi": true, "svg": true, "png": true})
	if err != nil {
		t.Fatal(err)
	}
	for i := range one {
		if !bytes.Equal(one[i], two[i]) {
			t.Fatal("Extra formats are nondeterministic")
		}
	}
}
