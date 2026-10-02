package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "calibrate" {
		if err := runCalibration(os.Args[2:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "charfall:", err)
			os.Exit(1)
		}
		return
	}
	opts := options{}
	output := ""
	overwrite := false
	extras := false
	formats := ""
	flag.StringVar(&formats, "formats", "", "Write files in these comma-separated formats: "+allExportFormats+" (default: terminal output)")
	flag.BoolVar(&extras, "extra-exports", false, "Export all formats; cannot combine with --formats")
	flag.BoolVar(&opts.autoTone, "auto-tone", true, "Bounded automatic tone adjustment; explicit brightness/contrast overrides disable it")
	flag.StringVar(&opts.quality, "quality", "neighborhood", "Structural mode: tone, shape, or neighborhood (default)")
	flag.StringVar(&opts.quality, "mode", "neighborhood", "Alias for --quality: tone, shape, neighborhood (default)")
	flag.StringVar(&opts.style, "style", "classic", "Brightness style: classic (default), bright, hybrid, full-color")
	flag.Float64Var(&opts.spatial.Strength, "coherence", 1, "Neighborhood strength (0–3; 0 reproduces shape selection)")
	flag.StringVar(&opts.spatial.Complexity, "complexity", "balanced", "Neighborhood complexity cost: compact, balanced, quality, max")
	flag.StringVar(&opts.profilePath, "glyph-profile", "", "Measured font profile JSON; defaults to embedded Menlo at 10px")
	flag.StringVar(&opts.fontPath, "font", "", "TTF or OTF font file to measure automatically")
	flag.Float64Var(&opts.fontSize, "font-size", 10, "Size for --font calibration (6–32)")
	flag.Float64Var(&opts.settings.Brightness, "brightness", 1, "Combined brightness multiplier (0.1–3)")
	flag.Float64Var(&opts.settings.Contrast, "contrast", 1, "Local contrast multiplier (0–3)")
	flag.Float64Var(&opts.settings.EdgeStrength, "edge-strength", 1, "Directional edge sensitivity (0–3; 0 disables)")
	flag.IntVar(&opts.settings.ColorStep, "color-step", 8, "RGB quantization step (1–32)")
	flag.IntVar(&opts.settings.RunTolerance, "run-tolerance", 10, "Maximum grouped color channel difference (0–32)")
	flag.IntVar(&opts.columns, "columns", 0, "Character columns (default: fit terminal; 176 for exports or redirected output)")
	flag.Float64Var(&opts.aspect, "cell-aspect", .60205, "Character width / line height")
	flag.StringVar(&opts.background, "background", "#040711", "Background for transparency and HTML, as #RRGGBB")
	flag.StringVar(&output, "o", "", "Output filename for one format, directory for multiple formats")
	flag.StringVar(&output, "output", "", "Output filename for one format, directory for multiple formats")
	flag.BoolVar(&overwrite, "overwrite", false, "Replace existing generated files")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: charfall [options] IMAGE\n       charfall IMAGE [options]\n       charfall calibrate [options] FONT.ttf")
		flag.PrintDefaults()
	}

	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		args = append(append([]string{}, args[1:]...), args[0])
	}
	flag.CommandLine.Parse(args)
	formatsExplicit := false
	fontSizeExplicit := false
	columnsExplicit := false
	outputExplicit := false
	overwriteExplicit := false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "columns":
			columnsExplicit = true
		case "o", "output":
			outputExplicit = true
		case "overwrite":
			overwriteExplicit = true
		}
		if f.Name == "font-size" {
			fontSizeExplicit = true
		}
		if f.Name == "cell-aspect" {
			opts.aspectExplicit = true
		}
		if f.Name == "formats" {
			formatsExplicit = true
		}
		if f.Name == "brightness" || f.Name == "contrast" {
			opts.autoTone = false
		}
	})
	if !columnsExplicit {
		opts.columns = 176
	}
	if fontSizeExplicit && opts.fontPath == "" {
		fmt.Fprintln(os.Stderr, "charfall: --font-size requires --font")
		os.Exit(2)
	}
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	source := flag.Arg(0)
	title := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	if extras {
		if formatsExplicit {
			fmt.Fprintln(os.Stderr, "charfall: choose --formats or --extra-exports, not both")
			os.Exit(2)
		}
		formats = allExportFormats
	}
	if !formatsExplicit && !extras && (outputExplicit || overwriteExplicit) {
		fmt.Fprintln(os.Stderr, "charfall: --output and --overwrite require --formats or --extra-exports")
		os.Exit(2)
	}
	var selected map[string]bool
	if formatsExplicit || extras {
		var err error
		selected, err = parseExportFormats(formats)
		if err != nil {
			fmt.Fprintln(os.Stderr, "charfall:", err)
			os.Exit(2)
		}
	}
	if selected != nil && output == "" {
		output = title + "-ascii"
		if len(selected) == 1 {
			for format := range selected {
				output += "." + format
			}
		}
	}
	width, height, terminal := terminalSize(os.Stdout)
	if selected == nil && terminal && !columnsExplicit {
		opts.previewSize = [2]int{width, height}
	}
	img, orientation, err := loadImage(source)
	if err == nil {
		var ascii asciiOutput
		ascii, err = convert(img, orientation, opts)
		if err == nil {
			if selected == nil {
				text := plainFormat(ascii)
				if terminal && os.Getenv("TERM") != "dumb" && os.Getenv("NO_COLOR") == "" {
					text = ansiFormat(ascii)
				}
				_, err = fmt.Fprint(os.Stdout, text)
			} else {
				err = export(ascii, output, title, overwrite, selected)
				if err == nil {
					absolute, _ := filepath.Abs(output)
					_, err = fmt.Fprintf(os.Stderr, "Saved %d x %d ASCII, %d colors, %d grouped runs to %s\n", ascii.Columns, ascii.Rows, ascii.Colors, ascii.Runs, absolute)
				}
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "charfall:", err)
		os.Exit(1)
	}
}
