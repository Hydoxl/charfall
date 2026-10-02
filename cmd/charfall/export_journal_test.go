package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var journalTestFiles = []string{"index.html", "ascii.txt", "ascii.json", "art.mjs", "ascii.ansi", "ascii.svg", "ascii.png"}

func TestExportCrashHelper(t *testing.T) {
	dir := os.Getenv("ASCII_JOURNAL_TEST_DIR")
	if dir == "" {
		return
	}
	kind := os.Getenv("ASCII_JOURNAL_TEST_KIND")
	stop := func() {
		fmt.Println("ready")
		io.Copy(io.Discard, os.Stdin)
		t.Fatal("Parent did not terminate the crash helper")
	}
	calls := 0
	rename := func(from, to string) error {
		if err := os.Rename(from, to); err != nil {
			return err
		}
		calls++
		if kind == "recovery" && calls == 1 || kind == "publication" && calls == 6 || (kind == "fresh-publication" || kind == "selected-publication") && calls == 3 {
			stop()
		}
		return nil
	}
	if kind == "recovery" {
		lock, err := lockExportDirectory(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		stages, err := filepath.Glob(filepath.Join(dir, ".ascii-export-*"))
		if err != nil || len(stages) != 1 {
			t.Fatalf("Missing interrupted transaction: %v", stages)
		}
		journal, err := readExportJournal(stages[0], journalTestFiles)
		if err != nil {
			t.Fatal(err)
		}
		if err := restoreExportJournal(dir, stages[0], journal, rename); err != nil {
			t.Fatal(err)
		}
		t.Fatal("Recovery helper never reached its crash point")
	}
	write := func(path string, content []byte) error {
		if err := writeExportFile(path, content); err != nil {
			return err
		}
		if kind == "staging" {
			stop()
		}
		return nil
	}
	contents := [][]byte{[]byte("new HTML"), []byte("new text"), []byte("new JSON"), []byte("new module")}
	names := journalTestFiles
	if kind == "selected-publication" {
		names = []string{"ascii.ansi", "ascii.png", "index.html", "ascii.txt", "ascii.json", "art.mjs", "ascii.svg"}
		contents = [][]byte{[]byte("new ANSI"), []byte("new PNG")}
	}
	if err := publishExports(dir, names, contents, true, write, rename); err != nil {
		t.Fatal(err)
	}
	t.Fatal("Publication helper never reached its crash point")
}

func killExportHelper(t *testing.T, dir, kind string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestExportCrashHelper$")
	cmd.Env = append(os.Environ(), "ASCII_JOURNAL_TEST_DIR="+dir, "ASCII_JOURNAL_TEST_KIND="+kind)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	ready := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(output)
		ready <- scanner.Scan() && scanner.Text() == "ready"
	}()
	select {
	case ok := <-ready:
		if !ok {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatalf("Helper did not reach crash point: %s", stderr.String())
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("Helper timed out: %s", stderr.String())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("Crash helper exited successfully")
	}
}

func TestJournalRecoversKilledProcesses(t *testing.T) {
	for _, kind := range []string{"staging", "publication", "fresh-publication", "selected-publication", "repeated-recovery", "moved-directory"} {
		t.Run(kind, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "output")
			if err := os.Mkdir(dir, 0755); err != nil {
				t.Fatal(err)
			}
			names := append(append([]string{}, journalTestFiles...), "notes.txt")
			if kind == "fresh-publication" {
				names = []string{"notes.txt"}
			}
			for _, name := range names {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("old "+name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := exportSnapshot(t, dir)
			crashKind := kind
			if kind == "repeated-recovery" || kind == "moved-directory" {
				crashKind = "publication"
			}
			killExportHelper(t, dir, crashKind)
			if kind == "repeated-recovery" {
				killExportHelper(t, dir, "recovery")
			}
			if kind == "moved-directory" {
				moved := filepath.Join(parent, "moved-output")
				if err := os.Rename(dir, moved); err != nil {
					t.Fatal(err)
				}
				dir = moved
			}

			err := publishExports(dir, managedExportFilenames, [][]byte{[]byte("ignored")}, false, writeExportFile, os.Rename)
			if kind == "fresh-publication" {
				if err != nil {
					t.Fatal(err)
				}
				before["index.html"] = "ignored"
			} else if err == nil || !strings.Contains(err.Error(), "output already contains") {
				t.Fatalf("Unexpected post-recovery overwrite result: %v", err)
			}
			if after := exportSnapshot(t, dir); !reflect.DeepEqual(before, after) {
				t.Fatalf("Crash recovery failed: before=%v after=%v", before, after)
			}
		})
	}
}

func TestJournalKeepsCommittedOutputAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	stage, err := os.MkdirTemp(dir, ".ascii-export-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ascii.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	journal := exportJournal{Version: 1, Phase: "committed", Files: []string{"ascii.txt"}, Previous: []bool{true}, NewCount: 1}
	if err := saveExportJournal(stage, journal); err != nil {
		t.Fatal(err)
	}

	if err := recoverExportJournals(dir, []string{"ascii.txt"}); err != nil {
		t.Fatal(err)
	}
	if got := exportSnapshot(t, dir); !reflect.DeepEqual(got, map[string]string{"ascii.txt": "new"}) {
		t.Fatalf("Committed output was rolled back or cleanup failed: %v", got)
	}
}

func TestJournalRejectsUnsafeRecoveryData(t *testing.T) {
	for _, problem := range []string{"traversal", "duplicate", "unknown-phase", "missing-backup", "symlink-backup", "legacy-stage", "malformed", "symlink-journal"} {
		t.Run(problem, func(t *testing.T) {
			dir := t.TempDir()
			stage, err := os.MkdirTemp(dir, ".ascii-export-")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "ascii.txt"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			previous := filepath.Join(stage, "previous")
			if err := os.Mkdir(previous, 0700); err != nil {
				t.Fatal(err)
			}
			journal := exportJournal{Version: 1, Phase: "prepared", Files: []string{"ascii.txt"}, Previous: []bool{true}, NewCount: 1}
			switch problem {
			case "traversal":
				journal.Files[0] = "../ascii.txt"
			case "duplicate":
				journal.Files = []string{"ascii.txt", "ascii.txt"}
				journal.Previous = []bool{true, true}
			case "unknown-phase":
				journal.Phase = "unknown"
			case "symlink-backup":
				if err := os.Symlink(filepath.Join(dir, "ascii.txt"), filepath.Join(previous, "ascii.txt")); err != nil {
					t.Fatal(err)
				}
			}
			if problem == "symlink-journal" {
				if err := os.Symlink(filepath.Join(dir, "ascii.txt"), filepath.Join(stage, "journal.json")); err != nil {
					t.Fatal(err)
				}
			} else if problem != "legacy-stage" {
				data, err := json.Marshal(journal)
				if err != nil {
					t.Fatal(err)
				}
				if problem == "malformed" {
					data = []byte(`{"version":`)
				}
				if err := os.WriteFile(filepath.Join(stage, "journal.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := recoverExportJournals(dir, []string{"ascii.txt"}); err == nil {
				t.Fatal("Unsafe journal was accepted")
			}
			if data, err := os.ReadFile(filepath.Join(dir, "ascii.txt")); err != nil || string(data) != "keep" {
				t.Fatal("Rejected recovery changed visible files")
			}
			if _, err := os.Stat(stage); err != nil {
				t.Fatal("Rejected recovery discarded its files")
			}
		})
	}
}

func TestJournalCleansUnpublishedOrphans(t *testing.T) {
	for _, kind := range []string{"empty", "partial-marker", "staging"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "ascii.txt"), []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			stage, err := os.MkdirTemp(dir, ".ascii-export-")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "partial-marker" {
				if err := os.WriteFile(filepath.Join(stage, ".journal-partial"), []byte("partial"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "staging" {
				journal := exportJournal{Version: 1, Phase: "staging", Files: []string{"ascii.txt"}, Previous: []bool{true}, NewCount: 1}
				if err := saveExportJournal(stage, journal); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stage, "ascii.txt"), []byte("partial"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := recoverExportJournals(dir, []string{"ascii.txt"}); err != nil {
				t.Fatal(err)
			}
			if got := exportSnapshot(t, dir); !reflect.DeepEqual(got, map[string]string{"ascii.txt": "old"}) {
				t.Fatalf("Orphan cleanup changed visible output: %v", got)
			}
		})
	}
}

func TestJournalDirectoryLockExcludesAnotherWriter(t *testing.T) {
	dir := t.TempDir()
	lock, err := lockExportDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = publishExports(dir, []string{"ascii.txt"}, [][]byte{[]byte("new")}, true, writeExportFile, os.Rename)
	if err == nil || !strings.Contains(err.Error(), "cannot lock") {
		lock.Close()
		t.Fatalf("Second writer was accepted: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := publishExports(dir, []string{"ascii.txt"}, [][]byte{[]byte("new")}, true, writeExportFile, os.Rename); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ascii.txt")); errors.Is(err, os.ErrNotExist) {
		t.Fatal("Lock was not released")
	}
}
