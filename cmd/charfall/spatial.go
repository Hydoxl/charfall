package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

type assignment struct {
	glyph                  byte
	rgb                    [3]int
	background             [3]int
	backgroundSet          bool
	score, error, coverage float64
	lab                    [3]float64
	backgroundLab          [3]float64
}

func assembleASCII(ascii *asciiOutput, tolerance int, cell func(int) *assignment) {
	ascii.Lines = make([][]run, ascii.Rows)
	ascii.Runs, ascii.BrightnessMAE = 0, 0
	colors := map[string]bool{}
	occupied, directional := 0, 0
	for y := 0; y < ascii.Rows; y++ {
		var text strings.Builder
		var anchor [3]int
		var backdrop [3]int
		backdropSet := false
		for x := 0; x < ascii.Columns; x++ {
			c := cell(y*ascii.Columns + x)
			if c.glyph != ' ' {
				occupied++
			}
			if strings.ContainsRune("/\\_|-", rune(c.glyph)) {
				directional++
			}
			ascii.BrightnessMAE += math.Abs(c.error)
			line := ascii.Lines[y]
			if len(line) > 0 && c.backgroundSet == backdropSet && (!c.backgroundSet || max(abs(c.background[0]-backdrop[0]), abs(c.background[1]-backdrop[1]), abs(c.background[2]-backdrop[2])) <= tolerance) && max(abs(c.rgb[0]-anchor[0]), abs(c.rgb[1]-anchor[1]), abs(c.rgb[2]-anchor[2])) <= tolerance {
				text.WriteByte(c.glyph)
				line[len(line)-1][1] = text.String()
			} else {
				text.Reset()
				text.WriteByte(c.glyph)
				anchor = c.rgb
				backdrop, backdropSet = c.background, c.backgroundSet
				key := fmt.Sprintf("#%02x%02x%02x", c.rgb[0], c.rgb[1], c.rgb[2])
				colors[key] = true
				r := run{key, text.String()}
				if c.backgroundSet {
					r = append(r, fmt.Sprintf("#%02x%02x%02x", c.background[0], c.background[1], c.background[2]))
				}
				line = append(line, r)
			}
			ascii.Lines[y] = line
		}
		ascii.Runs += len(ascii.Lines[y])
	}
	ascii.Colors = len(colors)
	ascii.BrightnessMAE /= float64(ascii.Columns * ascii.Rows)
	ascii.DirectionalFraction = float64(directional) / float64(ascii.Columns*ascii.Rows)
	ascii.OccupiedFraction = float64(occupied) / float64(ascii.Columns*ascii.Rows)
}

type spatialSettings struct {
	Strength   float64 `json:"strength"`
	Complexity string  `json:"complexity"`
}
type spatialDiagnostics struct {
	Settings              spatialSettings `json:"settings"`
	Passes                int             `json:"passes"`
	GlyphChanges          int             `json:"glyphChanges"`
	ColorChanges          int             `json:"colorChanges"`
	MeanLocalCostIncrease float64         `json:"meanLocalCostIncrease"`
}
type sourceFeature struct {
	lab                              [3]float64
	luma, contrast, gradient, gx, gy float64
	edge                             byte
}

// Oklab's published linear-sRGB transform: https://bottosson.github.io/posts/oklab/
func oklab(rgb [3]float64) [3]float64 {
	for c, v := range rgb {
		if v <= .04045 {
			rgb[c] = v / 12.92
		} else {
			rgb[c] = math.Pow((v+.055)/1.055, 2.4)
		}
	}
	l := math.Cbrt(.4122214708*rgb[0] + .5363325363*rgb[1] + .0514459929*rgb[2])
	m := math.Cbrt(.2119034982*rgb[0] + .6806995451*rgb[1] + .1073969566*rgb[2])
	s := math.Cbrt(.0883024619*rgb[0] + .2817188376*rgb[1] + .6299787005*rgb[2])
	return [3]float64{.2104542553*l + .7936177850*m - .0040720468*s, 1.9779984951*l - 2.4285922050*m + .4505937099*s, .0259040371*l + .7827717662*m - .8086757660*s}
}
func inkLab(rgb [3]int) [3]float64 {
	return oklab([3]float64{float64(rgb[0]) / 255, float64(rgb[1]) / 255, float64(rgb[2]) / 255})
}
func distanceSquared(a, b [3]float64) float64 {
	return (a[0]-b[0])*(a[0]-b[0]) + (a[1]-b[1])*(a[1]-b[1]) + (a[2]-b[2])*(a[2]-b[2])
}
func similarity(a, b sourceFeature) float64 {
	dc, dl, dt := distanceSquared(a.lab, b.lab), math.Abs(a.luma-b.luma), math.Abs(a.contrast-b.contrast)
	if dc > .0064 || dl > .08 || dt > .06 {
		return 0
	}
	flat := max(0, 1-max(a.contrast, b.contrast)/.08) * max(0, 1-max(a.gradient, b.gradient)/.08)
	return flat * math.Exp(-dc/.0016-dl*dl/.001225-dt*dt/.000625)
}
func complexityWeight(mode string) (float64, error) {
	switch mode {
	case "", "balanced":
		return .00012, nil
	case "compact":
		return .0004, nil
	case "quality":
		return .000025, nil
	case "max":
		return 0, nil
	default:
		return 0, errors.New("complexity must be compact, balanced, quality, or max")
	}
}

func strokeDistances(profile glyphProfile) [128][128]float64 {
	var result [128][128]float64
	for _, a := range " .:cCoO8@/\\_|-" {
		for _, b := range " .:cCoO8@/\\_|-" {
			ma, mb := profile.Masks[string(a)], profile.Masks[string(b)]
			meanA, meanB := 0.0, 0.0
			for i := range ma {
				meanA += ma[i] / float64(len(ma))
				meanB += mb[i] / float64(len(mb))
			}
			cov, ea, eb := 0.0, 0.0, 0.0
			for i := range ma {
				da, db := ma[i]-meanA, mb[i]-meanB
				cov += da * db
				ea += da * da
				eb += db * db
			}
			result[a][b] = .5
			if ea*eb > 1e-12 {
				result[a][b] = (1 - max(-1, min(1, cov/math.Sqrt(ea*eb)))) / 2
			}
			if a == b {
				result[a][b] = 0
			}
		}
	}
	return result
}
func edgeLink(a, b sourceFeature, dx, dy int) float64 {
	if a.edge == 0 || b.edge == 0 || a.gradient < .07 || b.gradient < .07 || distanceSquared(a.lab, b.lab) > .01 {
		return 0
	}
	na, nb := math.Hypot(a.gx, a.gy), math.Hypot(b.gx, b.gy)
	if na == 0 || nb == 0 {
		return 0
	}
	alignment := (a.gx*b.gx + a.gy*b.gy) / (na * nb)
	across := math.Abs(a.gx*float64(dx)+a.gy*float64(dy)) / (na * math.Hypot(float64(dx), float64(dy)))
	if alignment < .9 || across > .6 {
		return 0
	}
	return (1 - across) * alignment
}

func refine(shortlists [][4]assignment, features []sourceFeature, width, height int, profile glyphProfile, bg [3]byte, rendering renderSettings, settings spatialSettings) ([]assignment, spatialDiagnostics) {
	selected := make([]assignment, len(shortlists))
	for i := range shortlists {
		for j := range shortlists[i] {
			shortlists[i][j].lab = inkLab(shortlists[i][j].rgb)
			if rendering.Style != "" {
				color := [3]int{int(bg[0]), int(bg[1]), int(bg[2])}
				if shortlists[i][j].backgroundSet {
					color = shortlists[i][j].background
				}
				shortlists[i][j].backgroundLab = inkLab(color)
			}
		}
		selected[i] = shortlists[i][0]
	}
	diagnostics := spatialDiagnostics{Settings: settings}
	if settings.Strength == 0 {
		return selected, diagnostics
	}
	complexity, _ := complexityWeight(settings.Complexity)
	strokes := strokeDistances(profile)
	peak := 0.0
	for _, v := range profile.Coverage {
		peak = max(peak, v)
	}
	bgLuma := luma([3]float64{float64(bg[0]) / 255, float64(bg[1]) / 255, float64(bg[2]) / 255})
	for pass := 0; pass < 2; pass++ {
		for step := range selected {
			i := step
			if pass == 1 {
				i = len(selected) - 1 - step
			}
			x, y := i%width, i/width
			var neighbors [8]int
			var gates, edges [8]float64
			n := 0
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if dx == 0 && dy == 0 || x+dx < 0 || x+dx >= width || y+dy < 0 || y+dy >= height {
						continue
					}
					j := (y+dy)*width + x + dx
					neighbors[n] = j
					if dx == 0 || dy == 0 {
						gates[n] = similarity(features[i], features[j])
					}
					edges[n] = edgeLink(features[i], features[j], dx, dy)
					n++
				}
			}
			cost := func(c assignment) float64 {
				spatial := 0.0
				for k := 0; k < n; k++ {
					other := selected[neighbors[k]]
					density := (c.coverage - other.coverage) / peak
					stable := 0.0
					if c.glyph != other.glyph {
						stable = .00035
					}
					spatial += gates[k] * (.002*density*density + .04*distanceSquared(c.lab, other.lab) + stable)
					if rendering.Style != "" {
						spatial += gates[k] * .04 * distanceSquared(c.backgroundLab, other.backgroundLab)
					}
					if neighbors[k]/width == y && max(abs(c.rgb[0]-other.rgb[0]), abs(c.rgb[1]-other.rgb[1]), abs(c.rgb[2]-other.rgb[2])) > rendering.RunTolerance {
						spatial += gates[k] * complexity
					}
					spatial += edges[k] * .0006 * strokes[c.glyph][other.glyph]
				}
				return c.score + settings.Strength*spatial/4
			}
			best, bestCost := shortlists[i][0], math.Inf(1)
			consider := func(c assignment) {

				if c.score > shortlists[i][0].score+.0015 {
					return
				}
				v := cost(c)
				if v < bestCost {
					best, bestCost = c, v
				}
			}
			for _, c := range shortlists[i] {
				consider(c)
				if rendering.Style == "full-color" {
					continue
				}
				for k := 0; k < n; k++ {
					other := selected[neighbors[k]]
					if gates[k] < .6 || max(abs(c.rgb[0]-other.rgb[0]), abs(c.rgb[1]-other.rgb[1]), abs(c.rgb[2]-other.rgb[2])) > 16 {
						continue
					}
					borrowed := c
					borrowed.rgb = other.rgb
					borrowed.lab = other.lab
					backdrop := bgLuma
					if c.backgroundSet {
						backdrop = luma([3]float64{float64(c.background[0]) / 255, float64(c.background[1]) / 255, float64(c.background[2]) / 255})
					}
					predicted := backdrop*(1-c.coverage) + c.coverage*luma([3]float64{float64(borrowed.rgb[0]) / 255, float64(borrowed.rgb[1]) / 255, float64(borrowed.rgb[2]) / 255})
					target := backdrop*(1-c.coverage) + c.coverage*luma([3]float64{float64(c.rgb[0]) / 255, float64(c.rgb[1]) / 255, float64(c.rgb[2]) / 255}) - c.error
					borrowed.error = predicted - target
					borrowed.score += 16*(borrowed.error*borrowed.error-c.error*c.error) + .06*distanceSquared(c.lab, borrowed.lab)
					consider(borrowed)
				}
			}
			selected[i] = best
		}
		diagnostics.Passes++
	}
	for i, c := range selected {
		original := shortlists[i][0]
		if c.glyph != original.glyph {
			diagnostics.GlyphChanges++
		}
		if c.rgb != original.rgb {
			diagnostics.ColorChanges++
		}
		diagnostics.MeanLocalCostIncrease += c.score - original.score
	}
	diagnostics.MeanLocalCostIncrease /= float64(len(selected))
	return selected, diagnostics
}
