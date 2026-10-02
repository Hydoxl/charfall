package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

func paintGlyph(dst *image.Alpha, face font.Face, glyph rune, x, y float64) error {
	dot := fixed.Point26_6{X: fixed.Int26_6(math.Round(x * 64)), Y: fixed.Int26_6(math.Round(y * 64))}
	bounds, mask, origin, _, ok := face.Glyph(dot, glyph)
	if !ok {
		return fmt.Errorf("cannot rasterize glyph %q", glyph)
	}
	draw.DrawMask(dst, bounds, image.Opaque, image.Point{}, mask, origin, draw.Over)
	return nil
}

func measureFont(path string, size float64) (glyphProfile, error) {
	var profile glyphProfile
	if !finiteRange(size, 6, 32) {
		return profile, errors.New("font size must be finite from 6 to 32")
	}
	info, err := os.Stat(path)
	if err != nil {
		return profile, err
	}
	const maxFontBytes = 32 << 20
	if !info.Mode().IsRegular() || info.Size() > maxFontBytes {
		return profile, errors.New("font must be a regular TTF or OTF file of at most 32 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return profile, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxFontBytes+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return profile, err
	}
	if len(data) > maxFontBytes {
		return profile, errors.New("font exceeds 32 MiB")
	}
	parsed, err := opentype.Parse(data)
	if err != nil {
		return profile, fmt.Errorf("cannot read TTF or OTF font: %w", err)
	}
	family, err := parsed.Name(nil, sfnt.NameIDFamily)
	if err != nil {
		return profile, fmt.Errorf("cannot read font family: %w", err)
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		return profile, err
	}
	defer face.Close()
	advance, ok := face.GlyphAdvance(' ')
	if !ok || !finiteRange(float64(advance)/64/size, .2, 1.5) {
		return profile, errors.New("font has invalid character spacing")
	}
	const glyphs = " .:cCoO8@/\\_|-"
	for _, glyph := range glyphs {
		index, err := parsed.GlyphIndex(nil, glyph)
		if err != nil || index == 0 {
			return profile, fmt.Errorf("font is missing glyph %q", glyph)
		}
		width, ok := face.GlyphAdvance(glyph)
		if !ok || math.Abs(float64(width-advance)/64) > .03 {
			return profile, errors.New("font must be monospace")
		}
	}
	metrics := face.Metrics()
	baseline := math.Round(((size-float64(metrics.Ascent+metrics.Descent)/64)/2+float64(metrics.Ascent)/64)*64) / 64
	spacing := float64(advance) / 64
	const repeats, maskWidth, maskHeight, phases = 128, 6, 10, 8
	width, height := int(math.Ceil(spacing*repeats)), int(math.Ceil(size))
	rasterBounds := image.Rect(-1, -2, int(math.Ceil(spacing))+1, height+2)
	for _, glyph := range glyphs {
		bounds, _, ok := face.GlyphBounds(glyph)
		if !ok {
			return profile, fmt.Errorf("cannot measure glyph %q", glyph)
		}
		for _, phase := range []float64{0, float64(phases-1) / phases} {
			pixels := image.Rect(int(math.Floor(phase+float64(bounds.Min.X)/64)), int(math.Floor(baseline+float64(bounds.Min.Y)/64)),
				int(math.Ceil(phase+float64(bounds.Max.X)/64)), int(math.Ceil(baseline+float64(bounds.Max.Y)/64)))
			rasterBounds = rasterBounds.Union(pixels.Inset(-1))
		}
	}
	if rasterBounds.Dx() > 64 || rasterBounds.Dy() > 64 || rasterBounds.Min.X < -8 || rasterBounds.Min.Y < -8 || !finiteRange(baseline, 0, 64) {
		return profile, errors.New("font has unsupported glyph geometry")
	}
	profile = glyphProfile{FontFamily: family, FontSize: size, FontAdvance: spacing / size, FontData: data,
		Coverage: map[string]float64{}, MaskSize: []int{maskWidth, maskHeight}, Masks: map[string][]float64{},
		Method: "Go OpenType grayscale alpha; 6x10 stroke masks; 128 fractional cell positions; 8 raster phases; line height 1",
		Raster: &glyphRaster{Width: rasterBounds.Dx(), Height: rasterBounds.Dy(), Phases: phases,
			OffsetX: rasterBounds.Min.X, OffsetY: rasterBounds.Min.Y, Baseline: baseline, LineHeight: 1, Alpha: map[string][][]byte{}}}
	for _, glyph := range glyphs {
		key := string(glyph)
		bitmap := image.NewAlpha(image.Rect(0, 0, width, height))
		for cell := 0; cell < repeats; cell++ {
			if err := paintGlyph(bitmap, face, glyph, float64(cell)*spacing, baseline); err != nil {
				return profile, err
			}
		}
		ink := 0.0
		for _, alpha := range bitmap.Pix {
			ink += float64(alpha) / 255
		}
		profile.Coverage[key] = ink / (spacing * repeats * size)
		mask := make([]float64, maskWidth*maskHeight)
		dx, dy := spacing/maskWidth, size/maskHeight
		for cell := 0; cell < repeats; cell++ {
			for y := 0; y < maskHeight; y++ {
				for x := 0; x < maskWidth; x++ {
					left, top := float64(cell)*spacing+float64(x)*dx, float64(y)*dy
					right, bottom := left+dx, top+dy
					alpha := 0.0
					for py := int(math.Floor(top)); py < int(math.Ceil(bottom)); py++ {
						for px := int(math.Floor(left)); px < int(math.Ceil(right)); px++ {
							area := (min(float64(px+1), right) - max(float64(px), left)) * (min(float64(py+1), bottom) - max(float64(py), top))
							alpha += float64(bitmap.AlphaAt(px, py).A) / 255 * area
						}
					}
					mask[y*maskWidth+x] += alpha / (dx * dy * repeats)
				}
			}
		}
		for i := range mask {
			mask[i] = math.Round(max(0, min(1, mask[i]))*1e6) / 1e6
		}
		profile.Masks[key] = mask
		for phase := 0; phase < phases; phase++ {
			bitmap := image.NewAlpha(image.Rect(0, 0, rasterBounds.Dx(), rasterBounds.Dy()))
			if err := paintGlyph(bitmap, face, glyph, -float64(rasterBounds.Min.X)+float64(phase)/phases, baseline-float64(rasterBounds.Min.Y)); err != nil {
				return profile, err
			}
			profile.Raster.Alpha[key] = append(profile.Raster.Alpha[key], bitmap.Pix)
		}
	}
	return profile, validateProfile(profile, true)
}

func runCalibration(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("charfall calibrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	size := flags.Float64("size", 10, "Font size from 6 to 32")
	output := "-"
	flags.StringVar(&output, "o", "-", "Profile JSON filename; defaults to stdout")
	flags.StringVar(&output, "output", "-", "Profile JSON filename; defaults to stdout")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: charfall calibrate [options] FONT.ttf\n       charfall calibrate FONT.ttf [options]")
		flags.PrintDefaults()
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(append([]string{}, args[1:]...), args[0])
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("calibrate requires one TTF or OTF font file")
	}
	profile, err := measureFont(flags.Arg(0), *size)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if output == "-" {
		n, err := stdout.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	if err := writeSyncedExportFile(file, data); err != nil {
		return errors.Join(err, os.Remove(output))
	}
	return nil
}

func fontFaceCSS(profile *glyphProfile) string {
	if profile == nil || len(profile.FontData) == 0 {
		return ""
	}
	mime := "font/ttf"
	if bytes := profile.FontData; len(bytes) >= 4 && string(bytes[:4]) == "OTTO" {
		mime = "font/otf"
	}
	return fmt.Sprintf(`@font-face{font-family:"%s";font-weight:400;font-style:normal;src:url("data:%s;base64,%s")}`, profile.FontFamily, mime, base64.StdEncoding.EncodeToString(profile.FontData))
}
