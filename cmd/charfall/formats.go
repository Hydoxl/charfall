package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/png"
	"math"
	"strings"
)

type glyphRaster struct {
	Width      int                 `json:"width"`
	Height     int                 `json:"height"`
	Phases     int                 `json:"phases"`
	OffsetX    int                 `json:"offsetX"`
	OffsetY    int                 `json:"offsetY,omitempty"`
	Baseline   float64             `json:"baseline"`
	LineHeight float64             `json:"lineHeight"`
	Alpha      map[string][][]byte `json:"alpha"`
}

func validateRaster(r *glyphRaster) error {
	if r.Width < 1 || r.Width > 64 || r.Height < 1 || r.Height > 64 || r.Phases < 1 || r.Phases > 16 || r.OffsetX < -8 || r.OffsetX > 8 || r.OffsetY < -8 || r.OffsetY > 8 || !finiteRange(r.Baseline, 0, 64) || r.LineHeight != 1 {
		return errors.New("invalid native raster geometry or baseline; line height must be 1")
	}
	for _, g := range " .:cCoO8@/\\_|-" {
		phases := r.Alpha[string(g)]
		if len(phases) != r.Phases {
			return errors.New("missing raster phases for a supported glyph")
		}
		for _, alpha := range phases {
			if len(alpha) != r.Width*r.Height {
				return errors.New("incorrect native raster alpha dimensions")
			}
		}
	}
	return nil
}
func rasterProfile(ascii asciiOutput) (glyphProfile, error) {
	if ascii.Profile != nil && ascii.Profile.Raster != nil {
		return *ascii.Profile, nil
	}
	var profile glyphProfile
	if err := json.Unmarshal(defaultRasterProfile, &profile); err != nil {
		return profile, err
	}
	if ascii.Profile != nil && (ascii.Profile.FontFamily != profile.FontFamily || ascii.Profile.FontSize != profile.FontSize || math.Abs(ascii.Profile.FontAdvance-profile.FontAdvance) > .00001) {
		return profile, errors.New("PNG export needs raster alpha for this font; use --font or charfall calibrate")
	}
	return profile, validateRaster(profile.Raster)
}

func ansiFormat(ascii asciiOutput) string {
	bg, _ := parseBackground(ascii.Background)
	var ansi strings.Builder
	for _, line := range ascii.Lines {
		fmt.Fprintf(&ansi, "\x1b[48;2;%d;%d;%dm", bg[0], bg[1], bg[2])
		background := ascii.Background
		for _, run := range line {
			backdrop := ascii.Background
			if len(run) > 2 {
				backdrop = run[2]
			}
			if backdrop != background {
				colorBG, _ := parseBackground(backdrop)
				fmt.Fprintf(&ansi, "\x1b[48;2;%d;%d;%dm", colorBG[0], colorBG[1], colorBG[2])
				background = backdrop
			}
			ink, _ := parseBackground(run[0])
			fmt.Fprintf(&ansi, "\x1b[38;2;%d;%d;%dm%s", ink[0], ink[1], ink[2], run[1])
		}
		// Scrolling must use the terminal background rather than the ASCII background.
		ansi.WriteString("\x1b[0m\n")
	}
	return ansi.String()
}

func extraFormats(ascii asciiOutput, title string, selected map[string]bool) ([][]byte, error) {
	result := make([][]byte, 3)
	bg, _ := parseBackground(ascii.Background)
	if selected["ansi"] {
		result[0] = []byte(ansiFormat(ascii))
	}
	if !selected["svg"] && !selected["png"] {
		return result, nil
	}
	profile, err := rasterProfile(ascii)
	if err != nil {
		return nil, err
	}
	advance := profile.FontSize * profile.FontAdvance
	lineHeight := advance / ascii.CellAspect
	w, h := int(math.Ceil(float64(ascii.Columns)*advance)), int(math.Ceil(float64(ascii.Rows)*lineHeight))
	if w < 1 || h < 1 || selected["png"] && w > 40_000_000/h {
		return nil, errors.New("raster export exceeds forty million pixels; reduce columns")
	}
	var raster *image.RGBA
	if selected["png"] {
		raster = image.NewRGBA(image.Rect(0, 0, w, h))
		for i := 0; i < len(raster.Pix); i += 4 {
			copy(raster.Pix[i:i+4], []byte{bg[0], bg[1], bg[2], 255})
		}
	}
	var backgrounds strings.Builder
	if css := fontFaceCSS(ascii.Profile); css != "" {
		backgrounds.WriteString("<style>" + css + "</style>")
	}
	// Paint every cell background first so later spans cannot erase glyph overflow.
	for y, line := range ascii.Lines {
		column := 0
		for _, run := range line {
			if len(run) > 2 {
				colorBG, _ := parseBackground(run[2])
				left, right := int(math.Ceil(float64(column)*advance)), int(math.Ceil(float64(column+len(run[1]))*advance))
				top, bottom := int(math.Ceil(float64(y)*lineHeight)), int(math.Ceil(float64(y+1)*lineHeight))
				if raster != nil {
					for yy := top; yy < min(h, bottom); yy++ {
						for xx := left; xx < min(w, right); xx++ {
							raster.SetRGBA(xx, yy, color.RGBA{colorBG[0], colorBG[1], colorBG[2], 255})
						}
					}
				}
				fmt.Fprintf(&backgrounds, `<rect x="%g" y="%g" width="%g" height="%g" fill="%s"/>`, float64(column)*advance, float64(y)*lineHeight, float64(len(run[1]))*advance, lineHeight, run[2])
			}
			column += len(run[1])
		}
	}
	var svg strings.Builder
	fmt.Fprintf(&svg, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img"><title>%s</title><rect width="100%%" height="100%%" fill="%s"/>%s<g font-family="%s, Consolas, monospace" font-size="%g" font-weight="400" font-kerning="none" style="font-variant-ligatures:none;font-feature-settings:'liga' 0,'calt' 0;white-space:pre" xml:space="preserve">`, w, h, w, h, html.EscapeString(title), ascii.Background, backgrounds.String(), html.EscapeString(profile.FontFamily), profile.FontSize)
	for y, line := range ascii.Lines {
		column := 0
		fmt.Fprintf(&svg, `<text y="%g">`, float64(y)*lineHeight+profile.Raster.Baseline+(lineHeight-profile.FontSize)/2)
		for _, run := range line {
			ink, _ := parseBackground(run[0])
			fmt.Fprintf(&svg, `<tspan x="%g" fill="%s">%s</tspan>`, float64(column)*advance, run[0], html.EscapeString(run[1]))
			if raster == nil {
				column += len(run[1])
				continue
			}
			for _, g := range run[1] {
				origin := float64(column) * advance
				left := int(math.Floor(origin))
				phase := int(math.Round((origin - float64(left)) * float64(profile.Raster.Phases)))
				if phase == profile.Raster.Phases {
					phase = 0
					left++
				}
				alpha := profile.Raster.Alpha[string(g)][phase]
				top := int(math.Round(float64(y)*lineHeight+(lineHeight-profile.FontSize)/2)) + profile.Raster.OffsetY
				for py := 0; py < profile.Raster.Height; py++ {
					for px := 0; px < profile.Raster.Width; px++ {
						xx, yy := left+profile.Raster.OffsetX+px, top+py
						if xx < 0 || xx >= w || yy < 0 || yy >= h {
							continue
						}
						a := int(alpha[py*profile.Raster.Width+px])
						if a == 0 {
							continue
						}
						old := raster.RGBAAt(xx, yy)
						raster.SetRGBA(xx, yy, color.RGBA{byte((int(ink[0])*a + int(old.R)*(255-a) + 127) / 255), byte((int(ink[1])*a + int(old.G)*(255-a) + 127) / 255), byte((int(ink[2])*a + int(old.B)*(255-a) + 127) / 255), 255})
					}
				}
				column++
			}
		}
		svg.WriteString("</text>")
	}
	svg.WriteString("</g></svg>\n")
	if selected["svg"] {
		result[1] = []byte(svg.String())
	}
	if raster != nil {
		var encoded bytes.Buffer
		if err = png.Encode(&encoded, raster); err != nil {
			return nil, err
		}
		result[2] = encoded.Bytes()
	}
	return result, nil
}
