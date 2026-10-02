package main

import (
	"encoding/hex"
	"errors"
	"image"
	"math"
)

type options struct {
	columns        int
	aspect         float64
	background     string
	autoTone       bool
	quality        string
	style          string
	profilePath    string
	fontPath       string
	fontSize       float64
	aspectExplicit bool
	previewSize    [2]int
	settings       renderSettings
	spatial        spatialSettings
}
type metadata struct {
	Rendering           string              `json:"rendering,omitempty"`
	BrightnessStyle     string              `json:"brightnessStyle,omitempty"`
	Profile             *glyphProfile       `json:"glyphProfile,omitempty"`
	Settings            *renderSettings     `json:"settings,omitempty"`
	BrightnessMAE       float64             `json:"brightnessMAE,omitempty"`
	ToneAnalysis        *toneAnalysis       `json:"toneAnalysis,omitempty"`
	Neighborhood        *spatialDiagnostics `json:"neighborhood,omitempty"`
	Columns             int                 `json:"columns"`
	Rows                int                 `json:"rows"`
	CellAspect          float64             `json:"cellAspect"`
	SourceSize          [2]int              `json:"sourceSize"`
	Background          string              `json:"background"`
	Colors              int                 `json:"colors"`
	Runs                int                 `json:"runs"`
	DirectionalFraction float64             `json:"directionalFraction"`
	OccupiedFraction    float64             `json:"occupiedFraction"`
}

type run []string
type asciiOutput struct {
	Lines [][]run `json:"lines"`
	metadata
}

func parseBackground(s string) ([3]byte, error) {
	var rgb [3]byte
	if len(s) != 7 || s[0] != '#' {
		return rgb, errors.New("background must be #RRGGBB")
	}
	b, err := hex.DecodeString(s[1:])
	if err != nil {
		return rgb, errors.New("background must be #RRGGBB")
	}
	copy(rgb[:], b)
	return rgb, nil
}

func prepareOptions(opts *options) (profile glyphProfile, bg [3]byte, err error) {
	if opts.fontPath != "" && opts.profilePath != "" {
		return profile, bg, errors.New("choose --font or --glyph-profile, not both")
	}
	if opts.quality == "" {
		opts.quality = "neighborhood"
		if opts.spatial.Complexity == "" {
			opts.spatial = spatialSettings{Strength: 1, Complexity: "balanced"}
		}
	}
	if opts.style == "" {
		opts.style = "classic"
	}
	if opts.style != "classic" && opts.style != "bright" && opts.style != "hybrid" && opts.style != "full-color" {
		return profile, bg, errors.New("style must be classic, bright, hybrid, or full-color")
	}
	opts.settings.Style = ""
	if opts.style != "classic" {
		opts.settings.Style = opts.style
	}
	if opts.quality != "" && opts.quality != "tone" && opts.quality != "shape" && opts.quality != "neighborhood" {
		return profile, bg, errors.New("quality must be tone, shape, or neighborhood")
	}
	shaped := opts.quality == "shape" || opts.quality == "neighborhood"
	needsMasks := shaped || opts.style == "full-color"
	if opts.quality == "neighborhood" {
		if !finiteRange(opts.spatial.Strength, 0, 3) {
			return profile, bg, errors.New("coherence must be finite from 0 to 3")
		}
		if _, err := complexityWeight(opts.spatial.Complexity); err != nil {
			return profile, bg, err
		}
	}
	bg, err = parseBackground(opts.background)
	if err != nil {
		return profile, bg, err
	}
	if opts.columns < 1 {
		return profile, bg, errors.New("columns must be positive")
	}
	if opts.aspect <= 0 || math.IsNaN(opts.aspect) || math.IsInf(opts.aspect, 0) {
		return profile, bg, errors.New("cell aspect must be finite and positive")
	}
	if err = validateSettings(opts.settings); err != nil {
		return profile, bg, err
	}
	if opts.fontPath != "" {
		profile, err = measureFont(opts.fontPath, opts.fontSize)
	} else {
		profile, err = readProfile(opts.profilePath, needsMasks)
	}
	if err != nil {
		return profile, bg, err
	}
	if !opts.aspectExplicit && (opts.fontPath != "" || opts.profilePath != "") {
		opts.aspect = profile.FontAdvance
	}
	if needsMasks && math.Abs(opts.aspect-profile.FontAdvance) > .00001 {
		return profile, bg, errors.New("shape quality requires --cell-aspect matching the profile fontAdvance (measured line height 1)")
	}
	return profile, bg, nil
}

type cellGradient struct{ x, y, strength float32 }

func imageGradients(cells [][5]float32, width, height int) []cellGradient {
	gradients := make([]cellGradient, len(cells))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			i := y*width + x
			if width > 1 {
				left, right := max(0, x-1), min(width-1, x+1)
				gradients[i].x = (cells[y*width+right][0] - cells[y*width+left][0]) / float32(right-left)
			}
			if height > 1 {
				top, bottom := max(0, y-1), min(height-1, y+1)
				gradients[i].y = (cells[bottom*width+x][0] - cells[top*width+x][0]) / float32(bottom-top)
			}
			gradients[i].strength = float32(math.Hypot(float64(gradients[i].x), float64(gradients[i].y)))
		}
	}
	return gradients
}

func renderCells(ascii *asciiOutput, cells, patches [][5]float32, smooth []byte, profile glyphProfile, bg [3]byte, opts options) {
	rows := ascii.Rows
	w, h := ascii.SourceSize[0], ascii.SourceSize[1]
	gradients := imageGradients(cells, opts.columns, rows)
	var shortlists [][4]assignment
	var features []sourceFeature
	if opts.quality == "neighborhood" {
		shortlists = make([][4]assignment, len(cells))
		features = make([]sourceFeature, len(cells))
	}
	var patch []float64
	if patches != nil {
		size := profile.MaskSize[0] * profile.MaskSize[1]
		if opts.style == "full-color" {
			size *= 4
		}
		patch = make([]float64, size)
	}

	var candidate assignment
	fit := func(i int) *assignment {
		x, y := i%opts.columns, i/opts.columns
		c := cells[i]
		contrast := float32(math.Sqrt(float64(max(float32(0), c[1]-c[0]*c[0]))))
		lifted := float32(.90) * float32(math.Pow(float64(c[0]), .68))
		edgeTone := max(0, min(1, float64(lifted)+.36*(float64(c[0])-float64(smooth[i])/255)+float64(float32(.07)*contrast)))
		edge := byte(0)
		neighbour := max(gradients[y*opts.columns+max(0, x-1)].strength, gradients[y*opts.columns+min(opts.columns-1, x+1)].strength, gradients[max(0, y-1)*opts.columns+x].strength, gradients[min(rows-1, y+1)*opts.columns+x].strength)
		edgeThreshold := .15
		if opts.settings.EdgeStrength == 0 {
			edgeThreshold = math.Inf(1)
		} else {
			edgeThreshold /= opts.settings.EdgeStrength
		}
		if float64(gradients[i].strength) > edgeThreshold && contrast > .055 && edgeTone > .16 && edgeTone < .89 && gradients[i].strength >= neighbour*float32(.98) {
			px, py := float64(gradients[i].x)/(float64(w)/float64(opts.columns)), float64(gradients[i].y)/(float64(h)/float64(rows))
			switch {
			case math.Abs(py) > 2.3*math.Abs(px):
				edge = '_'
			case math.Abs(px) > 2.3*math.Abs(py):
				edge = '|'
			case px*py < 0:
				edge = '\\'
			default:
				edge = '/'
			}
		}
		tone, local := cellTone(c, smooth[i], opts.settings)
		if patch != nil {
			mw, mh := profile.MaskSize[0], profile.MaskSize[1]
			_, gain := brightnessTarget(c, local, opts.settings)
			for py := 0; py < mh; py++ {
				for px := 0; px < mw; px++ {
					sub := patches[(y*mh+py)*opts.columns*mw+x*mw+px]
					patch[py*mw+px] = luma(sourceInk(sub))
					if opts.style == "full-color" {
						var rgb [3]float64
						for channel := range rgb {
							rgb[channel] = max(0, min(1, float64(sub[channel+2])*gain))
							patch[mw*mh+(py*mw+px)*3+channel] = rgb[channel]
						}
						patch[py*mw+px] = luma(rgb)
					}
				}
			}
		}
		var shortlist *[4]assignment
		if shortlists != nil {
			shortlist = &shortlists[i]
			features[i] = sourceFeature{lab: oklab([3]float64{float64(c[2]), float64(c[3]), float64(c[4])}), luma: float64(c[0]), contrast: float64(contrast), gradient: float64(gradients[i].strength), gx: float64(gradients[i].x), gy: float64(gradients[i].y), edge: edge}
		}
		candidate = fitCandidates(c, tone, local, edge, profile, bg, opts.settings, patch, shortlist)
		return &candidate
	}
	if shortlists == nil {
		assembleASCII(ascii, opts.settings.RunTolerance, fit)
		return
	}
	for i := range cells {
		fit(i)
	}
	selected, diagnostics := refine(shortlists, features, opts.columns, rows, profile, bg, opts.settings, opts.spatial)
	ascii.Rendering = "calibrated-neighborhood"
	ascii.Neighborhood = &diagnostics
	assembleASCII(ascii, opts.settings.RunTolerance, func(i int) *assignment { return &selected[i] })
}

func convert(img image.Image, orientation int, opts options) (asciiOutput, error) {
	var ascii asciiOutput
	profile, bg, err := prepareOptions(&opts)
	if err != nil {
		return ascii, err
	}
	needsMasks := opts.quality == "shape" || opts.quality == "neighborhood" || opts.style == "full-color"
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	if orientation >= 5 {
		w, h = h, w
	}
	if w < 1 || h < 1 {
		return ascii, errors.New("empty image")
	}
	if opts.previewSize[0] > 0 && opts.previewSize[1] > 0 {
		// Leave one column for wrapping and two rows for the trailing newline and prompt.
		width := max(1, opts.previewSize[0]-1)
		height := max(1, opts.previewSize[1]-2)
		fit := math.Floor(float64(height) * float64(w) / (opts.aspect * float64(h)))
		opts.columns = max(1, int(min(float64(width), fit)))
	}
	rowCount := math.Max(1, math.RoundToEven(float64(opts.columns)*opts.aspect*float64(h)/float64(w)))
	if math.IsInf(rowCount, 0) || rowCount > float64(1_000_000/opts.columns) {
		return ascii, errors.New("grid exceeds one million cells; reduce --columns")
	}
	rows := int(rowCount)
	ascii.metadata = metadata{Columns: opts.columns, Rows: rows, CellAspect: opts.aspect, SourceSize: [2]int{w, h}, Background: opts.background}
	cells := sample(img, orientation, opts.columns, rows, bg)
	var patches [][5]float32
	if needsMasks {
		maskCells := profile.MaskSize[0] * profile.MaskSize[1]
		if len(cells) > 8_000_000/maskCells {
			return ascii, errors.New("shape sampling exceeds eight million subcells; reduce --columns or mask size")
		}
		patches = sample(img, orientation, opts.columns*profile.MaskSize[0], rows*profile.MaskSize[1], bg)
	}
	smooth := smoothLuma(cells, opts.columns, rows)
	originalSettings := opts.settings
	if opts.autoTone {
		opts.settings = chooseAutoTone(cells, smooth, profile, bg, opts.settings)
	}
	ascii.BrightnessStyle = opts.style
	ascii.Rendering = "calibrated-joint"
	if opts.quality == "shape" {
		ascii.Rendering = "calibrated-shape"
	}
	ascii.Profile = &profile
	ascii.Settings = &opts.settings
	renderCells(&ascii, cells, patches, smooth, profile, bg, opts)
	analysis := analyzeTone(cells, ascii, profile, bg)
	analysis.Automatic = opts.autoTone
	if opts.autoTone {
		analysis.Reason = "no safe improvement over original settings"
		if analysis.Source.DynamicRange < .03 {
			analysis.Reason = "near-flat source; retained original settings"
		}
	}
	if opts.autoTone && opts.settings != originalSettings {

		baselineOptions := opts
		baselineOptions.autoTone = false
		baselineOptions.settings = originalSettings
		baseline, err := convert(img, orientation, baselineOptions)
		if err != nil {
			return ascii, err
		}
		analysis.Before = &baseline.ToneAnalysis.Rendered
		if !safeTone(baseline.ToneAnalysis.Rendered, analysis.Rendered, analysis.Source) {
			analysis.Rendered = baseline.ToneAnalysis.Rendered
			analysis.Reason = "final rendering guard retained original settings"
			baseline.ToneAnalysis = &analysis
			return baseline, nil
		}
		analysis.Adjusted = true
		analysis.Reason = "bounded adjustment passed final rendering checks"
	}
	ascii.ToneAnalysis = &analysis
	return ascii, nil
}
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
