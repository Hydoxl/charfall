package main

import "math"

type sourceTones struct {
	Mean         float64    `json:"mean"`
	Percentiles  [5]float64 `json:"percentiles01_05_50_95_99"`
	DynamicRange float64    `json:"dynamicRange05_95"`
}
type renderedTones struct {
	Mean                     float64 `json:"mean"`
	SourceMAE                float64 `json:"sourceBrightnessMAE"`
	ShadowContrast           float64 `json:"shadowContrast"`
	HighlightContrast        float64 `json:"highlightContrast"`
	LostShadowDetailFraction float64 `json:"lostShadowDetailFraction"`
	LostHighlightFraction    float64 `json:"lostHighlightDetailFraction"`
	HighlightClippedFraction float64 `json:"highlightClippedFraction"`
	UnreachableFraction      float64 `json:"foregroundUnreachableFraction"`
	BackgroundFraction       float64 `json:"backgroundAssistedFraction"`
	BackgroundColors         int     `json:"backgroundColors"`
}
type toneAnalysis struct {
	Automatic bool           `json:"automatic"`
	Adjusted  bool           `json:"adjusted"`
	Reason    string         `json:"reason,omitempty"`
	Source    sourceTones    `json:"source"`
	Before    *renderedTones `json:"before,omitempty"`
	Rendered  renderedTones  `json:"rendered"`
}

func sourceDistribution(cells [][5]float32) sourceTones {
	var histogram [1024]int
	var result sourceTones
	for _, c := range cells {
		v := max(0, min(1, float64(c[0])))
		result.Mean += v / float64(len(cells))
		histogram[min(1023, int(v*1024))]++
	}
	for i, quantile := range [5]float64{.01, .05, .5, .95, .99} {
		count, rank := 0, max(1, int(math.Ceil(quantile*float64(len(cells)))))
		for bin, n := range histogram {
			count += n
			if count >= rank {
				result.Percentiles[i] = float64(bin) / 1023
				break
			}
		}
	}
	result.DynamicRange = result.Percentiles[3] - result.Percentiles[1]
	return result
}

type toneAccumulator struct {
	count, highlights, clipped int
	mean, error                float64

	bins [32][3]float64
}

func (a *toneAccumulator) add(source, predicted, ceiling float64) {
	a.count++
	a.mean += predicted
	a.error += math.Abs(predicted - source)
	bin := &a.bins[min(31, int(max(0, min(1, source))*32))]
	bin[0]++
	bin[1] += source
	bin[2] += predicted

	if source >= .7 && source < .98 {
		a.highlights++
		if predicted >= ceiling-.004 {
			a.clipped++
		}
	}
}

func (a toneAccumulator) measure() renderedTones {
	r := renderedTones{Mean: a.mean / float64(a.count), SourceMAE: a.error / float64(a.count)}
	if a.highlights > 0 {
		r.HighlightClippedFraction = float64(a.clipped) / float64(a.highlights)
	}

	detail := func(lo, hi int) (float64, float64) {
		previous := -1
		span, contrast, pairs, lost := 0.0, 0.0, 0.0, 0.0
		for i := lo; i <= hi; i++ {
			b := a.bins[i]
			if b[0] == 0 {
				continue
			}
			if previous >= 0 {
				p := a.bins[previous]
				ds, dr := b[1]/b[0]-p[1]/p[0], b[2]/b[0]-p[2]/p[0]
				if ds >= .004 {
					span += ds
					contrast += max(0, dr)
					pairs++
					if dr < .001 {
						lost++
					}
				}
			}
			previous = i
		}
		if span == 0 {
			return 0, 0
		}
		return contrast / span, lost / pairs
	}
	r.ShadowContrast, r.LostShadowDetailFraction = detail(0, 7)
	r.HighlightContrast, r.LostHighlightFraction = detail(23, 30)
	return r
}

func toneBackdrop(profile glyphProfile, bg [3]byte) (float64, float64) {
	b := luma([3]float64{float64(bg[0]) / 255, float64(bg[1]) / 255, float64(bg[2]) / 255})
	peak := 0.0
	for _, glyph := range " .:cCoO8@" {
		peak = max(peak, profile.Coverage[string(glyph)])
	}
	return b, b + (1-b)*peak
}

func analyzeTone(cells [][5]float32, ascii asciiOutput, profile glyphProfile, bg [3]byte) toneAnalysis {
	b, ceiling := toneBackdrop(profile, bg)
	var a toneAccumulator
	unreachable, assisted := 0, 0
	backgrounds := map[string]bool{}
	i := 0
	for _, line := range ascii.Lines {
		for _, run := range line {
			backdrop := b
			if len(run) > 2 {
				color, _ := parseBackground(run[2])
				backdrop = luma([3]float64{float64(color[0]) / 255, float64(color[1]) / 255, float64(color[2]) / 255})
				backgrounds[run[2]] = true
			}
			ink, _ := parseBackground(run[0])
			brightness := luma([3]float64{float64(ink[0]) / 255, float64(ink[1]) / 255, float64(ink[2]) / 255})
			for _, glyph := range run[1] {
				coverage := profile.Coverage[string(glyph)]
				if float64(cells[i][0]) > ceiling {
					unreachable++
				}
				if len(run) > 2 {
					assisted++
				}
				clipCeiling := ceiling
				if ascii.BrightnessStyle == "hybrid" || ascii.BrightnessStyle == "full-color" {
					clipCeiling = 1
				}
				a.add(float64(cells[i][0]), backdrop*(1-coverage)+coverage*brightness, clipCeiling)
				i++
			}
		}
	}
	rendered := a.measure()
	rendered.UnreachableFraction = float64(unreachable) / float64(len(cells))
	rendered.BackgroundFraction = float64(assisted) / float64(len(cells))
	rendered.BackgroundColors = len(backgrounds)
	return toneAnalysis{Source: sourceDistribution(cells), Rendered: rendered}
}

func safeTone(before, after renderedTones, source sourceTones) bool {

	limit := .04
	if source.Mean < .18 || source.Percentiles[2] < .12 || source.Percentiles[2] > .7 {
		limit = .02
	}
	return after.Mean-before.Mean <= limit && after.Mean >= before.Mean-.005 &&
		after.SourceMAE <= before.SourceMAE+.00001 &&
		after.ShadowContrast >= before.ShadowContrast*.9 &&
		after.HighlightContrast >= before.HighlightContrast*.9 &&
		after.LostShadowDetailFraction <= before.LostShadowDetailFraction+.02 &&
		after.LostHighlightFraction <= before.LostHighlightFraction+.02 &&
		after.HighlightClippedFraction <= before.HighlightClippedFraction+.005
}

func chooseAutoTone(cells [][5]float32, smooth []byte, profile glyphProfile, bg [3]byte, settings renderSettings) renderSettings {
	distribution := sourceDistribution(cells)
	if distribution.DynamicRange < .03 {
		return settings
	}
	count := min(1024, len(cells))
	b, ceiling := toneBackdrop(profile, bg)
	evaluate := func(s renderSettings) renderedTones {
		var a toneAccumulator
		for j := 0; j < count; j++ {
			i := min(len(cells)-1, int((float64(j)+.5)*float64(len(cells))/float64(count)))
			tone, local := cellTone(cells[i], smooth[i], s)
			candidate := fitCandidates(cells[i], tone, local, 0, profile, bg, s, nil, nil)
			coverage := candidate.coverage
			ink := luma([3]float64{float64(candidate.rgb[0]) / 255, float64(candidate.rgb[1]) / 255, float64(candidate.rgb[2]) / 255})
			backdrop := b
			if s.Style != "" {
				c := candidate
				if c.backgroundSet {
					backdrop = luma([3]float64{float64(c.background[0]) / 255, float64(c.background[1]) / 255, float64(c.background[2]) / 255})
				}
			}
			clipCeiling := ceiling
			if s.Style == "hybrid" || s.Style == "full-color" {
				clipCeiling = 1
			}
			a.add(float64(cells[i][0]), backdrop*(1-coverage)+coverage*ink, clipCeiling)
		}
		return a.measure()
	}
	baseline := evaluate(settings)
	score := func(r renderedTones, s renderSettings) float64 {
		return r.SourceMAE + .02*(r.LostShadowDetailFraction+r.LostHighlightFraction+r.HighlightClippedFraction) + .001*math.Pow(s.AutoLift/.09, 2) + .001*math.Abs(s.Contrast-settings.Contrast)
	}
	best, bestScore := settings, score(baseline, settings)
	for _, lift := range []float64{0, .03, .06, .09} {
		for _, contrast := range []float64{.9, 1, 1.1} {
			s := settings
			s.AutoLift, s.Contrast = lift, settings.Contrast*contrast
			r := evaluate(s)
			if cost := score(r, s); safeTone(baseline, r, distribution) && cost < bestScore-.0002 {
				best, bestScore = s, cost
			}
		}
	}
	return best
}
