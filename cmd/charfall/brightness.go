package main

import "math"

const hybridLimit = .35

func brightnessTarget(cell [5]float32, local float64, settings renderSettings) ([3]float64, float64) {
	rgb := [3]float64{float64(cell[2]), float64(cell[3]), float64(cell[4])}
	mean := luma(rgb)
	target := max(0, min(1, (mean+local*.25)*settings.Brightness))
	shoulder := max(0, min(1, (.75-target)/.2))
	target += settings.AutoLift * 4 * target * (1 - target) * shoulder * shoulder * (3 - 2*shoulder)
	gain := 1.0
	if mean > 0 {
		gain = target / mean
	}
	for channel := range rgb {
		rgb[channel] = max(0, min(1, rgb[channel]*gain))
	}
	return rgb, gain
}

func fittedPair(mask, patch []float64, target [3]float64) ([3]float64, [3]float64) {
	var foreground, background [3]float64
	meanA, meanAA := 0.0, 0.0
	for _, a := range mask {
		meanA += a / float64(len(mask))
		meanAA += a * a / float64(len(mask))
	}
	bb, ab := 1-2*meanA+meanAA, meanA-meanAA
	for channel := 0; channel < 3; channel++ {
		y, ay := 0.0, 0.0
		for i, a := range mask {
			v := target[channel]
			if len(patch) == len(mask)*4 {
				v = patch[len(mask)+i*3+channel]
			}
			y += v / float64(len(mask))
			ay += a * v / float64(len(mask))
		}
		f, b := y, y
		if determinant := meanAA*bb - ab*ab; determinant > 1e-12 {
			f = (ay*bb - (y-ay)*ab) / determinant
			b = ((y-ay)*meanAA - ay*ab) / determinant
		}

		best := math.Inf(1)
		consider := func(fg, bg float64) {
			if fg < 0 || fg > 1 || bg < 0 || bg > 1 {
				return
			}
			error := meanAA*fg*fg + bb*bg*bg + 2*ab*fg*bg - 2*ay*fg - 2*(y-ay)*bg
			if error < best {
				best = error
				foreground[channel] = fg
				background[channel] = bg
			}
		}
		consider(f, b)
		for _, boundary := range []float64{0, 1} {
			fg, bg := y, y
			if bb > 1e-12 {
				bg = max(0, min(1, (y-ay-ab*boundary)/bb))
			}
			if meanAA > 1e-12 {
				fg = max(0, min(1, (ay-ab*boundary)/meanAA))
			}
			consider(boundary, bg)
			consider(fg, boundary)
		}
	}
	return foreground, background
}

func fitBrightnessCandidates(cell [5]float32, tone, local float64, edge byte, profile glyphProfile, bg [3]byte, settings renderSettings, patch []float64, shortlist *[4]assignment) assignment {
	target, _ := brightnessTarget(cell, local, settings)
	background := [3]float64{float64(bg[0]) / 255, float64(bg[1]) / 255, float64(bg[2]) / 255}
	b, ceiling := toneBackdrop(profile, bg)
	peak := (ceiling - b) / (1 - b)
	if b == 1 {
		peak = profile.Coverage["@"]
	}
	preferred := peak * math.Sqrt(max(0, min(1, tone*settings.Brightness)))
	if luma(target) > ceiling {
		preferred = peak
	}
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
	for _, glyph := range candidates {
		a := profile.Coverage[string(glyph)]
		fg, bgColor := target, background
		mask := profile.Masks[string(glyph)]
		if settings.Style == "full-color" {
			fg, bgColor = fittedPair(mask, patch, target)
		} else {
			if settings.Style == "hybrid" && luma(target) > ceiling && a < 1 {

				denominator := (1 - a) * max(1e-12, luma(target)-b)
				mix := max(0, min(hybridLimit, (luma(target)-a-(1-a)*b)/denominator))
				for c := range bgColor {
					bgColor[c] += mix * (target[c] - bgColor[c])
				}
			}
			if a > 0 {
				for c := range fg {
					fg[c] = max(0, min(1, (target[c]-(1-a)*bgColor[c])/a))
				}
			}
		}
		ink, backdrop := quantize(fg, settings.ColorStep), [3]int{int(bg[0]), int(bg[1]), int(bg[2])}
		assisted := settings.Style == "full-color" || bgColor != background
		if assisted {
			backdrop = quantize(bgColor, settings.ColorStep)
		}
		predicted := [3]float64{}
		colorError := 0.0
		for c := range predicted {
			predicted[c] = (1-a)*float64(backdrop[c])/255 + a*float64(ink[c])/255
			colorError += (predicted[c] - target[c]) * (predicted[c] - target[c]) / 3
		}
		error := luma(predicted) - luma(target)
		score := 16*error*error + 2*colorError + .012*math.Pow((a-preferred)/max(peak, 1e-12), 2)
		if settings.Style == "full-color" && len(patch) == len(mask)*4 {
			pixelError := 0.0
			for i, alpha := range mask {
				for c := 0; c < 3; c++ {
					value := alpha*float64(ink[c])/255 + (1-alpha)*float64(backdrop[c])/255
					delta := value - patch[len(mask)+i*3+c]
					pixelError += delta * delta / float64(len(mask)*3)
				}
			}
			score += 2 * pixelError
		} else if len(patch) > 0 && len(mask) == len(patch) && a > 0 {
			mean, energy, correlation, maskEnergy := 0.0, 0.0, 0.0, 0.0
			for _, v := range patch {
				mean += v / float64(len(patch))
			}
			for i, v := range patch {
				d := v - mean
				m := mask[i] - a
				energy += d * d
				maskEnergy += m * m
				correlation += m * d
			}
			if energy*maskEnergy > 1e-12 {
				score += .012 * min(1, math.Sqrt(energy/float64(len(patch)))/.2) * settings.Contrast * (1 - max(-1, min(1, correlation/math.Sqrt(energy*maskEnergy))))
			}
		}
		c := assignment{glyph: byte(glyph), rgb: ink, background: backdrop, backgroundSet: assisted, score: score, error: error, coverage: a}
		if score < best.score {
			best = c
		}
		if shortlist != nil {
			for i := range shortlist {
				if score < shortlist[i].score {
					for j := len(shortlist) - 1; j > i; j-- {
						shortlist[j] = shortlist[j-1]
					}
					shortlist[i] = c
					break
				}
			}
		}
	}
	return best
}
