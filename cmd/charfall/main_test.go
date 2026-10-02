package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIOutput(t *testing.T) {
	if os.Getenv("CHARFALL_TEST_CLI") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"charfall"}, os.Args[i+1:]...)
				break
			}
		}
		flag.CommandLine = flag.NewFlagSet("charfall", flag.ExitOnError)
		main()
		os.Exit(0)
	}
	source := filepath.Join(t.TempDir(), "sample.png")
	var input bytes.Buffer
	if err := png.Encode(&input, image.NewNRGBA(image.Rect(0, 0, 8, 6))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, input.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		args    []string
		columns int
		files   []string
		error   string
	}{
		{name: "redirected defaults", columns: 176},
		{name: "explicit width", args: []string{"--columns", "12"}, columns: 12},
		{name: "explicit width above default", args: []string{"--columns", "240"}, columns: 240},
		{name: "single export", args: []string{"--formats", "png", "-o", "ascii.png"}, files: []string{"ascii.png"}},
		{name: "multiple exports", args: []string{"--formats", "html,txt"}, files: []string{"sample-ascii/index.html", "sample-ascii/ascii.txt"}},
		{name: "all exports", args: []string{"--extra-exports"}, files: []string{"sample-ascii/index.html", "sample-ascii/ascii.txt", "sample-ascii/ascii.json", "sample-ascii/ascii.ansi", "sample-ascii/ascii.svg", "sample-ascii/ascii.png"}},
		{name: "output needs formats", args: []string{"-o", "output"}, error: "require --formats"},
		{name: "overwrite needs formats", args: []string{"--overwrite"}, error: "require --formats"},
		{name: "empty formats", args: []string{"--formats="}, error: "unknown export format"},
		{name: "conflicting formats", args: []string{"--formats", "txt", "--extra-exports"}, error: "choose --formats"},
		{name: "invalid columns", args: []string{"--columns", "0"}, error: "columns must be"},
		{name: "grid resource limit", args: []string{"--columns", "1000001"}, error: "grid exceeds one million cells"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			args := append([]string{"-test.run=^TestCLIOutput$", "--", source}, tc.args...)
			cmd := exec.Command(os.Args[0], args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "CHARFALL_TEST_CLI=1", "TERM=xterm-256color")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if tc.error != "" {
				if err == nil || !strings.Contains(stderr.String(), tc.error) || stdout.Len() != 0 {
					t.Fatalf("Expected %q: error=%v stdout=%q stderr=%q", tc.error, err, stdout.String(), stderr.String())
				}
			} else if err != nil {
				t.Fatalf("CLI failed: %v, %s", err, stderr.String())
			} else if tc.columns > 0 {
				if stderr.Len() != 0 || bytes.ContainsRune(stdout.Bytes(), '\x1b') || !strings.HasSuffix(stdout.String(), "\n") {
					t.Fatalf("Redirected output is not clean plain text: stderr=%q", stderr.String())
				}
				for _, line := range strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n") {
					if len(line) != tc.columns {
						t.Fatalf("Expected %d columns, got %d", tc.columns, len(line))
					}
				}
			} else if stdout.Len() != 0 || !strings.HasPrefix(stderr.String(), "Saved ") {
				t.Fatalf("Export status must be on stderr: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			var files []string
			if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
				if err == nil && !entry.IsDir() {
					rel, _ := filepath.Rel(dir, path)
					files = append(files, filepath.ToSlash(rel))
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if len(files) != len(tc.files) {
				t.Fatalf("Unexpected files: %v", files)
			}
			for _, name := range tc.files {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || len(data) == 0 {
					t.Fatalf("Missing or empty export %s: %v", name, err)
				}
				if strings.HasSuffix(name, ".png") {
					config, err := png.DecodeConfig(bytes.NewReader(data))
					if err != nil || config.Width != 1060 || config.Height != 791 {
						t.Fatalf("Unexpected default PNG geometry: %+v, %v", config, err)
					}
				}
			}
		})
	}
}

func TestTerminalSizing(t *testing.T) {
	opts := options{columns: 176, aspect: .60205, background: "#040711", quality: "tone",
		settings: renderSettings{Brightness: 1, Contrast: 1, ColorStep: 8}}
	for _, tc := range []struct {
		size        [2]int
		orientation int
		want        [2]int
	}{
		{[2]int{80, 24}, 1, [2]int{73, 22}},
		{[2]int{40, 12}, 6, [2]int{8, 10}},
		{[2]int{2, 3}, 1, [2]int{1, 1}},
		{[2]int{300, 120}, 1, [2]int{299, 90}},
		{[2]int{500, 80}, 1, [2]int{259, 78}},
	} {
		opts.previewSize = tc.size
		ascii, err := convert(image.NewNRGBA(image.Rect(0, 0, 24, 12)), tc.orientation, opts)
		if err != nil || [2]int{ascii.Columns, ascii.Rows} != tc.want {
			t.Fatalf("ASCII does not fit %v: %dx%d, %v", tc.size, ascii.Columns, ascii.Rows, err)
		}
	}
	opts.previewSize = [2]int{2001, 2001}
	if _, err := convert(image.NewNRGBA(image.Rect(0, 0, 24, 12)), 1, opts); err == nil || !strings.Contains(err.Error(), "grid exceeds one million cells") {
		t.Fatalf("Automatic sizing bypassed the grid resource limit: %v", err)
	}
	profile, err := readProfile("", false)
	if err != nil {
		t.Fatal(err)
	}
	profile.FontAdvance = .8
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	opts.profilePath = filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(opts.profilePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	opts.previewSize = [2]int{80, 24}
	ascii, err := convert(image.NewNRGBA(image.Rect(0, 0, 24, 12)), 6, opts)
	if err != nil || ascii.CellAspect != .8 || ascii.Columns != 13 || ascii.Rows != 21 {
		t.Fatalf("Sizing ignored custom font spacing: %dx%d aspect=%g, %v", ascii.Columns, ascii.Rows, ascii.CellAspect, err)
	}
}
