package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type exportJournal struct {
	Version  int      `json:"version"`
	Phase    string   `json:"phase"`
	Files    []string `json:"files"`
	Previous []bool   `json:"previous"`
	NewCount int      `json:"newCount"`
}

func syncExportPath(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func saveExportJournal(stage string, journal exportJournal) (err error) {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(stage, ".journal-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	var syncErr error
	if writeErr == nil {
		syncErr = file.Sync()
	}
	if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), filepath.Join(stage, "journal.json")); err != nil {
		return err
	}
	return syncExportPath(stage)
}

func readExportJournal(stage string, allowed []string) (exportJournal, error) {
	var journal exportJournal
	path := filepath.Join(stage, "journal.json")
	info, err := os.Lstat(path)
	if err != nil {
		return journal, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16_384 {
		return journal, errors.New("invalid export journal file")
	}
	file, err := os.Open(path)
	if err != nil {
		return journal, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 16_385))
	if err != nil {
		return journal, err
	}
	if len(data) > 16_384 {
		return journal, errors.New("export journal exceeds size limit")
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		return journal, err
	}
	if journal.Version != 1 || len(journal.Files) == 0 || len(journal.Files) != len(journal.Previous) || journal.NewCount < 1 || journal.NewCount > len(journal.Files) {
		return journal, errors.New("invalid export journal structure")
	}
	switch journal.Phase {
	case "staging", "prepared", "committed", "rolled-back":
	default:
		return journal, errors.New("unknown export journal phase")
	}
	known := map[string]bool{}
	for _, name := range allowed {
		known[name] = true
	}
	for _, name := range journal.Files {
		if !known[name] || filepath.Base(name) != name {
			return journal, errors.New("unexpected or duplicate filename in export journal")
		}
		delete(known, name)
	}
	return journal, nil
}

// Delete the completion marker last, so interrupted cleanup remains recognizable.
func cleanupExportStage(stage string) error {
	entries, err := os.ReadDir(stage)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "journal.json" {
			if err := os.RemoveAll(filepath.Join(stage, entry.Name())); err != nil {
				return err
			}
		}
	}
	if err := syncExportPath(stage); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(stage, "journal.json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(stage); err != nil {
		return err
	}
	return syncExportPath(filepath.Dir(stage))
}

func restoreExportJournal(destination, stage string, journal exportJournal, rename func(string, string) error) error {
	previous := filepath.Join(stage, "previous")
	info, err := os.Lstat(previous)
	if err != nil || !info.IsDir() {
		return errors.New("missing or invalid export backup directory")
	}

	for i, name := range journal.Files {
		if journal.Previous[i] {
			info, err := os.Lstat(filepath.Join(previous, name))
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("missing or invalid backup for %s", name)
			}
		}
		if info, err := os.Lstat(filepath.Join(destination, name)); err == nil {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("recovery target %s must be a regular file", name)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	for i, name := range journal.Files {
		target := filepath.Join(destination, name)
		if journal.Previous[i] {
			temporary := filepath.Join(stage, "restore-"+name)
			if err := os.Remove(temporary); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := os.Link(filepath.Join(previous, name), temporary); err != nil {
				return err
			}
			if err := rename(temporary, target); err != nil {
				return err
			}
		} else if i < journal.NewCount {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	if err := syncExportPath(destination); err != nil {
		return err
	}
	journal.Phase = "rolled-back"
	return saveExportJournal(stage, journal)
}

func recoverExportJournals(destination string, allowed []string) error {
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	stages := map[string]exportJournal{}
	prepared := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".ascii-export-") {
			continue
		}
		stage := filepath.Join(destination, entry.Name())
		if !entry.IsDir() {
			return fmt.Errorf("unexpected export staging path: %s", stage)
		}
		journal, err := readExportJournal(stage, allowed)
		if os.IsNotExist(err) {
			children, readErr := os.ReadDir(stage)
			onlyJournalTemps := readErr == nil
			for _, child := range children {
				if !strings.HasPrefix(child.Name(), ".journal-") || !child.Type().IsRegular() {
					onlyJournalTemps = false
				}
			}
			if onlyJournalTemps {
				stages[stage] = exportJournal{Phase: "staging"}
				continue
			}
		}
		if err != nil {
			return fmt.Errorf("cannot read export journal in %s; files retained: %w", stage, err)
		}
		if journal.Phase == "prepared" {
			prepared++
		}
		stages[stage] = journal
	}
	if prepared > 1 {
		return errors.New("multiple prepared export journals; recovery files retained for inspection")
	}
	for stage, journal := range stages {
		if journal.Phase == "prepared" {
			if err := restoreExportJournal(destination, stage, journal, os.Rename); err != nil {
				return fmt.Errorf("export recovery failed; files retained in %s: %w", stage, err)
			}
		}
		if err := cleanupExportStage(stage); err != nil {
			return fmt.Errorf("cannot clean export transaction %s: %w", stage, err)
		}
	}
	return nil
}
