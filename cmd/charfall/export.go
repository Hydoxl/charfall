package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"strings"
)

const allExportFormats = "html,txt,json,ansi,svg,png"

// Legacy names are retained only for journal recovery and overwrite cleanup.
var managedExportFilenames = []string{
	"index.html", "ascii.txt", "ascii.json", "ascii.ansi", "ascii.svg", "ascii.png",
	"art.txt", "art.json", "art.ansi", "art.svg", "art.png", "art.mjs",
}

func parseExportFormats(value string) (map[string]bool, error) {
	selected := map[string]bool{}
	for _, name := range strings.Split(value, ",") {
		name = strings.TrimSpace(name)
		switch name {
		case "html", "txt", "json", "ansi", "svg", "png":
			selected[name] = true
		default:
			return nil, fmt.Errorf("unknown export format %q; choose from %s", name, allExportFormats)
		}
	}
	return selected, nil
}

func plainFormat(ascii asciiOutput) string {
	var text strings.Builder
	for _, line := range ascii.Lines {
		for _, r := range line {
			text.WriteString(r[1])
		}
		text.WriteByte('\n')
	}
	return text.String()
}

func export(ascii asciiOutput, destination, title string, overwrite bool, selected map[string]bool) error {
	additional, err := extraFormats(ascii, title, selected)
	if err != nil {
		return err
	}
	var rendered strings.Builder
	for y, line := range ascii.Lines {
		if y > 0 {
			rendered.WriteByte('\n')
		}
		for _, r := range line {
			style := "color:" + r[0]
			if len(r) > 2 {
				style += ";background-color:" + r[2]
			}
			fmt.Fprintf(&rendered, `<span style="%s">%s</span>`, style, html.EscapeString(r[1]))
		}
	}
	label := html.EscapeString(title)
	advance, size, family := .60205, 10.0, "Menlo"
	if ascii.Profile != nil {
		advance, size, family = ascii.Profile.FontAdvance, ascii.Profile.FontSize, ascii.Profile.FontFamily
	}
	cellBackgroundCSS := fontFaceCSS(ascii.Profile)
	if ascii.BrightnessStyle == "hybrid" || ascii.BrightnessStyle == "full-color" {
		cellBackgroundCSS += fmt.Sprintf("pre span{display:inline-block;vertical-align:top;height:%gem}", advance/ascii.CellAspect)
	}
	page := fmt.Sprintf(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>%s</title><link rel="icon" href="data:,"><style>
:root{color-scheme:dark}body{margin:0;background:#080e1b;color:#aabbce;font:12px Arial,sans-serif}main{max-width:%gpx;margin:auto;padding:20px 12px}h1{overflow-wrap:anywhere;font-size:13px;font-weight:400;margin:0 0 20px}.stage{container-type:inline-size;background:%s}pre{margin:0;font-family:"%s",Consolas,monospace;font-size:min(%gpx,%gcqw);line-height:%g;letter-spacing:0;white-space:pre;font-variant-ligatures:none;font-feature-settings:'liga' 0,'calt' 0}
%s</style></head><body><main><h1>%s</h1><div class="stage" role="img" aria-label="Colored ASCII rendition of %s"><pre aria-hidden="true">%s</pre></div></main></body></html>`, label, float64(ascii.Columns)*advance*size+24, ascii.Background, family, size, 100/(float64(ascii.Columns)*advance), advance/ascii.CellAspect, cellBackgroundCSS, label, label, rendered.String())
	data, err := json.Marshal(ascii)
	if err != nil {
		return err
	}
	available := append([][]byte{[]byte(page), []byte(plainFormat(ascii)), data}, additional...)
	var filenames []string
	omitted := append([]string(nil), managedExportFilenames[6:]...)
	var contents [][]byte
	for i, name := range strings.Split(allExportFormats, ",") {
		filename := managedExportFilenames[i]
		if selected[name] {
			filenames = append(filenames, filename)
			contents = append(contents, available[i])
		} else {
			omitted = append(omitted, filename)
		}
	}
	if len(contents) == 0 {
		return errors.New("select at least one export format")
	}
	if len(contents) == 1 {
		return publishExportFile(destination, contents[0], overwrite, writeSyncedExportFile, os.Rename)
	}
	filenames = append(filenames, omitted...)
	return publishExports(destination, filenames, contents, overwrite, writeExportFile, os.Rename)
}
