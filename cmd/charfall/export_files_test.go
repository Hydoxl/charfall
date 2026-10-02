package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
)

func exportSnapshot(t *testing.T, directory string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := map[string]string{}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		snapshot[entry.Name()] = string(data)
	}
	return snapshot
}

func TestExportFailureRecovery(t *testing.T) {
	names := managedExportFilenames
	for _, populated := range []bool{false, true} {
		for _, count := range []int{2, 3, 6} {
			for _, phase := range []string{"write", "rename"} {
				steps := count
				if phase == "rename" && populated {
					steps = len(names)
				}
				for failAt := 0; failAt < steps; failAt++ {
					t.Run(fmt.Sprintf("existing=%t/files=%d/%s=%d", populated, count, phase, failAt), func(t *testing.T) {
						dir := t.TempDir()
						if populated {
							for _, name := range names {
								if err := os.WriteFile(filepath.Join(dir, name), []byte("old "+name), 0600); err != nil {
									t.Fatal(err)
								}
							}
						}
						if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("keep"), 0600); err != nil {
							t.Fatal(err)
						}
						before := exportSnapshot(t, dir)
						contents := make([][]byte, count)
						for i := range contents {
							contents[i] = []byte("new " + names[i])
						}
						writes, renames := 0, 0
						write := func(path string, data []byte) error {
							step := writes
							writes++
							if phase == "write" && step == failAt {
								if err := os.WriteFile(path, []byte("partial"), 0644); err != nil {
									return err
								}
								return syscall.ENOSPC
							}
							return writeExportFile(path, data)
						}
						rename := func(from, to string) error {
							step := renames
							renames++
							if phase == "rename" && step == failAt {
								return syscall.EIO
							}
							return os.Rename(from, to)
						}
						wantErr := syscall.ENOSPC
						if phase == "rename" {
							wantErr = syscall.EIO
						}
						if err := publishExports(dir, names, contents, true, write, rename); !errors.Is(err, wantErr) {
							t.Fatalf("Missing failure: %v", err)
						}
						if after := exportSnapshot(t, dir); !reflect.DeepEqual(before, after) {
							t.Fatalf("Failed export changed the original set: before=%v, after=%v", before, after)
						}
					})
				}
			}
		}
	}
}

func TestExportRetainsBackupOnRollbackFailure(t *testing.T) {
	dir := t.TempDir()
	names := []string{"index.html", "ascii.txt"}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("old "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	rename := func(from, to string) error {
		calls++
		if calls > 1 {
			return syscall.EIO
		}
		return os.Rename(from, to)
	}
	err := publishExports(dir, names, [][]byte{[]byte("new"), []byte("new")}, true, writeExportFile, rename)
	if err == nil || !strings.Contains(err.Error(), "recovery files retained") {
		t.Fatalf("Rollback failure omitted recovery location: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, ".ascii-export-*", "previous", "index.html"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("Original backup is missing: %v, %v", backups, err)
	}
	if data, err := os.ReadFile(backups[0]); err != nil || string(data) != "old index.html" {
		t.Fatalf("Backup was damaged: %q, %v", data, err)
	}
}

func TestExportPreservesFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ascii.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := publishExports(dir, []string{"ascii.txt"}, [][]byte{[]byte("new")}, true, writeExportFile, os.Rename); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("Replacement changed file permissions: %v, %v", info, err)
	}
}

func TestSingleExportFile(t *testing.T) {
	for _, kind := range []string{"fresh", "overwrite", "refuse", "write-failure", "install-failure", "symlink", "directory", "collision"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "custom.txt")
			for _, name := range []string{"notes.txt", "ascii.txt", "index.html"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "symlink" {
				if err := os.Symlink("notes.txt", path); err != nil {
					t.Fatal(err)
				}
			} else if kind == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if kind != "fresh" && kind != "collision" {
				if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write := func(file *os.File, content []byte) error {
				if kind == "write-failure" {
					file.Write([]byte("partial"))
					return syscall.ENOSPC
				}
				if kind == "collision" {
					if err := os.WriteFile(path, []byte("raced"), 0600); err != nil {
						return err
					}
				}
				return writeSyncedExportFile(file, content)
			}
			rename := func(from, to string) error {
				if kind == "install-failure" {
					return syscall.EIO
				}
				return os.Rename(from, to)
			}
			err := publishExportFile(path, []byte("new"), kind != "refuse" && kind != "collision", write, rename)
			success := kind == "fresh" || kind == "overwrite"
			if (err == nil) != success {
				t.Fatalf("Unexpected publication result: %v", err)
			}
			if kind == "directory" {
				info, err := os.Stat(path)
				if err != nil || !info.IsDir() {
					t.Fatal("Rejected directory target changed")
				}
			} else {
				want := "old"
				if success {
					want = "new"
				} else if kind == "symlink" {
					want = "keep"
				} else if kind == "collision" {
					want = "raced"
				}
				if got, err := os.ReadFile(path); err != nil || string(got) != want {
					t.Fatalf("Target was damaged: %q, %v", got, err)
				}
				if kind == "overwrite" {
					info, _ := os.Stat(path)
					if info.Mode().Perm() != 0600 {
						t.Fatal("Overwrite changed file permissions")
					}
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 4 {
				t.Fatalf("Temporary files remained or unrelated files disappeared: %v, %v", entries, err)
			}
			for _, name := range []string{"notes.txt", "ascii.txt", "index.html"} {
				if data, err := os.ReadFile(filepath.Join(dir, name)); err != nil || string(data) != "keep" {
					t.Fatalf("Single export changed another file: %s, %v", name, err)
				}
			}
		})
	}
}
