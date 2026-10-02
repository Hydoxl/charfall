package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"math"
	"os"
	"regexp"
)

//go:embed profiles/menlo-profile.json
var defaultProfile []byte

//go:embed profiles/menlo-shape-profile.json
var defaultShapeProfile []byte

//go:embed profiles/menlo-raster-profile.json
var defaultRasterProfile []byte

type glyphProfile struct {
	FontFamily  string               `json:"fontFamily"`
	FontSize    float64              `json:"fontSize"`
	FontAdvance float64              `json:"fontAdvance"`
	FontData    []byte               `json:"fontData,omitempty"`
	Coverage    map[string]float64   `json:"coverage"`
	Method      string               `json:"method"`
	MaskSize    []int                `json:"maskSize,omitempty"`
	Masks       map[string][]float64 `json:"masks,omitempty"`
	Raster      *glyphRaster         `json:"raster,omitempty"`
}
type renderSettings struct {
	Style        string  `json:"style,omitempty"`
	Brightness   float64 `json:"brightness"`
	Contrast     float64 `json:"contrast"`
	EdgeStrength float64 `json:"edgeStrength"`
	ColorStep    int     `json:"colorStep"`
	RunTolerance int     `json:"runTolerance"`
	AutoLift     float64 `json:"autoLift,omitempty"`
}

func readProfile(path string, shape bool) (glyphProfile, error) {
	data := defaultProfile
	if shape {
		data = defaultShapeProfile
	}
	var err error
	if path != "" {
		data, err = os.ReadFile(path)
		if err != nil {
			return glyphProfile{}, err
		}
	}
	var profile glyphProfile
	if err = json.Unmarshal(data, &profile); err != nil {
		return profile, err
	}
	return profile, validateProfile(profile, shape)
}

func validateProfile(profile glyphProfile, shape bool) error {
	if len(profile.FontData) > 32<<20 {
		return errors.New("embedded font exceeds 32 MiB")
	}
	if !regexp.MustCompile(`^[A-Za-z0-9 _-]{1,80}$`).MatchString(profile.FontFamily) || !finiteRange(profile.FontSize, 6, 32) || !finiteRange(profile.FontAdvance, .2, 1.5) {
		return errors.New("invalid font name, size, or advance in glyph profile")
	}
	for _, glyph := range " .:cCoO8@/\\_|-" {
		coverage, ok := profile.Coverage[string(glyph)]
		if !ok || !finiteRange(coverage, 0, 1) || glyph != ' ' && coverage == 0 {
			return errors.New("glyph profile must include valid coverage for every supported character")
		}
	}
	if profile.Coverage[" "] != 0 {
		return errors.New("space coverage must be zero")
	}
	if profile.Raster != nil {
		if err := validateRaster(profile.Raster); err != nil {
			return err
		}
	}
	if shape {
		if len(profile.MaskSize) != 2 {
			return errors.New("shape quality requires a two-dimensional mask size")
		}
		width, height := profile.MaskSize[0], profile.MaskSize[1]
		if width < 2 || width > 16 || height < 2 || height > 24 {
			return errors.New("shape quality requires glyph masks between 2x2 and 16x24")
		}
		for _, glyph := range " .:cCoO8@/\\_|-" {
			mask := profile.Masks[string(glyph)]
			if len(mask) != width*height {
				return errors.New("missing or incorrectly sized glyph mask")
			}
			mean := 0.0
			for _, alpha := range mask {
				if !finiteRange(alpha, 0, 1) {
					return errors.New("glyph mask values must be finite alpha from 0 to 1")
				}
				mean += alpha / float64(len(mask))
			}
			if math.Abs(mean-profile.Coverage[string(glyph)]) > .02 {
				return errors.New("glyph mask mean does not match coverage")
			}
		}
	}
	return nil
}
func finiteRange(v, lo, hi float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= lo && v <= hi
}
func validateSettings(s renderSettings) error {
	if !finiteRange(s.AutoLift, 0, .09) || !finiteRange(s.Brightness, .1, 3) || !finiteRange(s.Contrast, 0, 3) || !finiteRange(s.EdgeStrength, 0, 3) || s.ColorStep < 1 || s.ColorStep > 32 || s.RunTolerance < 0 || s.RunTolerance > 32 {
		return errors.New("brightness must be 0.1–3, contrast/edge strength 0–3, color step 1–32, and run tolerance 0–32")
	}
	return nil
}
func luma(rgb [3]float64) float64 { return .2126*rgb[0] + .7152*rgb[1] + .0722*rgb[2] }
func sourceInk(cell [5]float32) [3]float64 {
	var rgb [3]float64
	for channel, v := range cell[2:] {
		rgb[channel] = float64(float32(math.Pow(float64(v), .81)) * float32(.96))
	}
	return rgb
}
func cellTone(c [5]float32, smooth byte, settings renderSettings) (float64, float64) {
	contrast := float32(math.Sqrt(float64(max(float32(0), c[1]-c[0]*c[0]))))
	lifted := float32(.90) * float32(math.Pow(float64(c[0]), .68))
	local := (float64(c[0]) - float64(smooth)/255) * settings.Contrast
	return max(0, min(1, float64(lifted)+.36*local+float64(float32(.07)*contrast))), local
}
func quantize(rgb [3]float64, step int) [3]int {
	var result [3]int
	for channel, v := range rgb {
		value := int(max(0, min(1, v)) * 255)
		result[channel] = min(255, int(math.RoundToEven(float64(value)/float64(step)))*step)
	}
	return result
}

func fitCell(cell [5]float32, tone, local float64, edge byte, profile glyphProfile, bg [3]byte, settings renderSettings, patch []float64) (byte, [3]int, float64) {
	c := fitCandidates(cell, tone, local, edge, profile, bg, settings, patch, nil)
	return c.glyph, c.rgb, c.error
}

func fitCandidates(cell [5]float32, tone, local float64, edge byte, profile glyphProfile, bg [3]byte, settings renderSettings, patch []float64, shortlist *[4]assignment) assignment {
	if settings.Style != "" {
		return fitBrightnessCandidates(cell, tone, local, edge, profile, bg, settings, patch, shortlist)
	}
	base := sourceInk(cell)
	baseLuma := luma(base)
	peak := 0.0
	for _, glyph := range " .:cCoO8@" {
		peak = max(peak, profile.Coverage[string(glyph)])
	}
	backdrop := [3]float64{float64(bg[0]) / 255, float64(bg[1]) / 255, float64(bg[2]) / 255}
	backgroundLuma := luma(backdrop)

	normalized := max(0, min(1, (baseLuma+local*.25)*settings.Brightness))

	shoulder := max(0, min(1, (.75-normalized)/.2))
	shoulder = shoulder * shoulder * (3 - 2*shoulder)
	normalized += settings.AutoLift * 4 * normalized * (1 - normalized) * shoulder
	target := backgroundLuma + (1-backgroundLuma)*peak*normalized
	preferred := peak * math.Sqrt(max(0, min(1, tone*settings.Brightness)))
	candidates := " .:cCoO8@"
	if edge != 0 {
		candidates = string(edge) + candidates
	}
	best := assignment{score: math.Inf(1)}
	if shortlist != nil {
		for i := range shortlist {
			shortlist[i].score = math.Inf(1)
		}
	}
	patchMean, patchEnergy := 0.0, 0.0
	for _, v := range patch {
		patchMean += v / float64(len(patch))
	}
	for _, v := range patch {
		patchEnergy += (v - patchMean) * (v - patchMean)
	}
	for _, glyph := range candidates {
		coverage := profile.Coverage[string(glyph)]
		required := 0.0
		rgb := base
		if coverage > 0 {
			required = max(0, (target-backgroundLuma*(1-coverage))/coverage)
			gain := 0.0
			if baseLuma > 0 {
				gain = required / baseLuma
			}
			largest := max(base[0], base[1], base[2])
			if largest > 0 {
				gain = min(gain, 1/largest)
			}
			for c := range rgb {
				rgb[c] *= gain
			}
		}
		ink := quantize(rgb, settings.ColorStep)
		predicted := backgroundLuma*(1-coverage) + coverage*luma([3]float64{float64(ink[0]) / 255, float64(ink[1]) / 255, float64(ink[2]) / 255})
		error := predicted - target
		densityPenalty := math.Pow((coverage-preferred)/peak, 2) * .012

		if edge != 0 && byte(glyph) == edge {
			densityPenalty *= .15
		}
		score := error*error*16 + densityPenalty
		if patchEnergy > 1e-12 && coverage > 0 {
			mask := profile.Masks[string(glyph)]
			maskMean, energy, correlation := 0.0, 0.0, 0.0
			for _, alpha := range mask {
				maskMean += alpha / float64(len(mask))
			}
			for i, alpha := range mask {
				deviation := alpha - maskMean
				energy += deviation * deviation
				correlation += deviation * (patch[i] - patchMean)
			}
			if energy > 1e-12 {
				correlation = max(-1, min(1, correlation/math.Sqrt(energy*patchEnergy)))

				weight := .012 * min(1, math.Sqrt(patchEnergy/float64(len(patch)))/.2) * settings.Contrast
				score += weight * (1 - correlation)
			}
		}
		c := assignment{glyph: byte(glyph), rgb: ink, score: score, error: error, coverage: coverage}
		if score < best.score {
			best = c
		}
		if shortlist != nil {
			for index := range shortlist {
				if score < shortlist[index].score {
					for j := len(shortlist) - 1; j > index; j-- {
						shortlist[j] = shortlist[j-1]
					}
					shortlist[index] = c
					break
				}
			}
		}
	}
	return best
}
