package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExportOverwrite(t *testing.T) {
	ascii := asciiOutput{Lines: [][]run{{{"#ffffff", "@"}}}, metadata: metadata{
		Columns: 1, Rows: 1, CellAspect: .60205, Background: "#040711",
	}}
	all, _ := parseExportFormats(allExportFormats)
	defaults, _ := parseExportFormats("html,txt")
	dir := t.TempDir()
	if err := export(ascii, dir, "first", false, all); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "art.mjs"), []byte("legacy module"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	ascii.Lines[0][0][1] = " "
	if err := export(ascii, dir, "second", false, defaults); err == nil {
		t.Fatal("Existing output was overwritten without permission")
	}
	if data, err := os.ReadFile(filepath.Join(dir, "ascii.txt")); err != nil || string(data) != "@\n" {
		t.Fatalf("Rejected overwrite changed the original: %q, %v", data, err)
	}
	if err := export(ascii, dir, "second <title>", true, defaults); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ascii.json", "art.mjs", "ascii.ansi", "ascii.svg", "ascii.png"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("Stale extra export remains: %s, %v", name, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "ascii.txt")); err != nil || string(data) != " \n" {
		t.Fatalf("Text export was not updated: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "notes.txt")); err != nil || string(data) != "keep" {
		t.Fatalf("Unrelated file changed: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "index.html")); err != nil || !strings.Contains(string(data), "second &lt;title&gt;") {
		t.Fatalf("HTML title was not escaped: %v", err)
	}
	if err := os.Symlink("notes.txt", filepath.Join(dir, "ascii.png")); err != nil {
		t.Fatal(err)
	}
	if err := export(ascii, dir, "third", true, defaults); err == nil {
		t.Fatal("Overwrite accepted a symlink among stale exports")
	}
	if data, err := os.ReadFile(filepath.Join(dir, "index.html")); err != nil || !strings.Contains(string(data), "second &lt;title&gt;") {
		t.Fatal("Rejected symlink export changed existing output")
	}
}

func TestExportFormatSelection(t *testing.T) {
	ascii := asciiOutput{Lines: [][]run{{{"#ffffff", "@"}}}, metadata: metadata{
		Columns: 1, Rows: 1, CellAspect: .60205, Background: "#040711",
	}}
	all, _ := parseExportFormats(allExportFormats)
	reference := t.TempDir()
	if err := export(ascii, reference, "test", false, all); err != nil {
		t.Fatal(err)
	}
	for _, value := range append(strings.Split(allExportFormats, ","), "html,txt", "png,ansi", " txt ,txt ") {
		t.Run(value, func(t *testing.T) {
			selected, err := parseExportFormats(value)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			destination := dir
			if len(selected) == 1 {
				for name := range selected {
					filename := "ascii." + name
					if name == "html" {
						filename = "index.html"
					}
					destination = filepath.Join(dir, filename)
				}
			}
			if err := export(ascii, destination, "test", false, selected); err != nil {
				t.Fatal(err)
			}
			got := exportSnapshot(t, dir)
			if len(got) != len(selected) {
				t.Fatalf("Unexpected files for %s: %v", value, got)
			}
			for name := range selected {
				filename := "ascii." + name
				if name == "html" {
					filename = "index.html"
				}
				want, err := os.ReadFile(filepath.Join(reference, filename))
				if err != nil || !bytes.Equal([]byte(got[filename]), want) {
					t.Fatalf("Selected output differs from complete export: %s, %v", filename, err)
				}
			}

			if err := export(ascii, reference, "test", true, all); err != nil {
				t.Fatal(err)
			}
			target := reference
			before := exportSnapshot(t, reference)
			if len(selected) == 1 {
				target = filepath.Join(reference, filepath.Base(destination))
			}
			if err := export(ascii, target, "test", true, selected); err != nil {
				t.Fatal(err)
			}
			want := got
			if len(selected) == 1 {
				want = before
			}
			if after := exportSnapshot(t, reference); !reflect.DeepEqual(want, after) {
				t.Fatalf("Overwrite left stale formats: %v", after)
			}
			if err := export(ascii, reference, "test", true, all); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, invalid := range []string{"", "html,", "pdf", "../txt", "HTML", "mjs", "html,mjs"} {
		if _, err := parseExportFormats(invalid); err == nil {
			t.Fatalf("Accepted invalid formats: %q", invalid)
		}
	}

	ascii.Profile = &glyphProfile{FontFamily: "Other", FontSize: 12, FontAdvance: .6}
	selected, _ := parseExportFormats("ansi")
	if err := export(ascii, filepath.Join(t.TempDir(), "ascii"), "test", false, selected); err != nil {
		t.Fatal(err)
	}
	selected, _ = parseExportFormats("png")
	if err := export(ascii, filepath.Join(t.TempDir(), "ascii"), "test", false, selected); err == nil {
		t.Fatal("PNG accepted an incompatible raster profile")
	}
}

func TestLegacyExportNames(t *testing.T) {
	dir := t.TempDir()
	legacy := managedExportFilenames[6:]
	stage, err := os.MkdirTemp(dir, ".ascii-export-")
	if err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(stage, "previous")
	if err := os.Mkdir(previous, 0700); err != nil {
		t.Fatal(err)
	}
	journal := exportJournal{Version: 1, Phase: "prepared", Files: legacy, Previous: make([]bool, len(legacy)), NewCount: len(legacy)}
	for i, name := range legacy {
		journal.Previous[i] = true
		if err := os.WriteFile(filepath.Join(previous, name), []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("interrupted"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveExportJournal(stage, journal); err != nil {
		t.Fatal(err)
	}
	ascii := asciiOutput{Lines: [][]run{{{"#ffffff", "@"}}}, metadata: metadata{
		Columns: 1, Rows: 1, CellAspect: .60205, Background: "#040711",
	}}
	selected, _ := parseExportFormats("html,txt")
	if err := export(ascii, dir, "legacy", false, selected); err == nil {
		t.Fatal("Legacy files were replaced without overwrite permission")
	}
	for _, name := range legacy {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(data) != "original" {
			t.Fatalf("Legacy journal did not recover %s: %q, %v", name, data, err)
		}
	}
	if err := export(ascii, dir, "legacy", true, selected); err != nil {
		t.Fatal(err)
	}
	files := exportSnapshot(t, dir)
	if len(files) != 2 || files["ascii.txt"] != "@\n" || files["index.html"] == "" {
		t.Fatalf("Legacy exports were not replaced with ASCII filenames: %v", files)
	}
}

func TestANSIFormat(t *testing.T) {
	ascii := asciiOutput{Lines: [][]run{
		{{"#ff0000", "@", "#0000ff"}, {"#00ff00", "."}},
		{{"#ffffff", " "}},
	}, metadata: metadata{Background: "#040711"}}
	want := "\x1b[48;2;4;7;17m\x1b[48;2;0;0;255m\x1b[38;2;255;0;0m@" +
		"\x1b[48;2;4;7;17m\x1b[38;2;0;255;0m.\x1b[0m\n" +
		"\x1b[48;2;4;7;17m\x1b[38;2;255;255;255m \x1b[0m\n"
	if got := ansiFormat(ascii); got != want {
		t.Fatalf("ANSI export has incorrect colors, rows, or reset: %q", got)
	}
}
