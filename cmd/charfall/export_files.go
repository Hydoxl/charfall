package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func writeExportFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	return writeSyncedExportFile(file, content)
}

func writeSyncedExportFile(file *os.File, content []byte) error {
	_, writeErr := file.Write(content)
	var syncErr error
	if writeErr == nil {
		syncErr = file.Sync()
	}
	return errors.Join(writeErr, syncErr, file.Close())
}

func publishExportFile(destination string, content []byte, overwrite bool, write func(*os.File, []byte) error, rename func(string, string) error) (err error) {
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	directory, err := lockExportDirectory(parent)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	if err := recoverExportJournals(parent, managedExportFilenames); err != nil {
		return err
	}
	mode := os.FileMode(0644)
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("output must be a regular file; use a filename for a single format")
		}
		if !overwrite {
			return errors.New("output file already exists; choose another filename or use --overwrite")
		}
		mode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(parent, ".ascii-file-")
	if err != nil {
		return err
	}
	defer file.Close()
	defer func() {
		if removeErr := os.Remove(file.Name()); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, removeErr)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if err := write(file, content); err != nil {
		return err
	}
	if overwrite {
		err = rename(file.Name(), destination)
	} else {

		err = os.Link(file.Name(), destination)
	}
	if err != nil {
		return err
	}
	return syncExportPath(parent)
}

func publishExports(destination string, filenames []string, contents [][]byte, overwrite bool, write func(string, []byte) error, rename func(string, string) error) (err error) {
	if err := os.MkdirAll(destination, 0755); err != nil {
		return err
	}
	directory, err := lockExportDirectory(destination)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	if err := recoverExportJournals(destination, filenames); err != nil {
		return err
	}
	existing := make([]os.FileInfo, len(filenames))
	journal := exportJournal{Version: 1, Phase: "staging", Files: append([]string(nil), filenames...), Previous: make([]bool, len(filenames)), NewCount: len(contents)}
	for i, name := range filenames {
		info, statErr := os.Lstat(filepath.Join(destination, name))
		if statErr != nil {
			if !os.IsNotExist(statErr) {
				return statErr
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return errors.New("output files must be regular files")
		}
		if !overwrite {
			return errors.New("output already contains converter files; choose another directory or use --overwrite")
		}
		existing[i] = info
		journal.Previous[i] = true
	}
	stage, err := os.MkdirTemp(destination, ".ascii-export-")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			err = errors.Join(err, cleanupExportStage(stage))
		}
	}()
	if err := saveExportJournal(stage, journal); err != nil {
		return err
	}
	if err := syncExportPath(destination); err != nil {
		return err
	}
	previous := filepath.Join(stage, "previous")
	if err := os.Mkdir(previous, 0700); err != nil {
		return err
	}
	for i, content := range contents {
		if err := write(filepath.Join(stage, filenames[i]), content); err != nil {
			return err
		}
	}
	for i, info := range existing {
		if info == nil {
			continue
		}
		name := filenames[i]
		backup := filepath.Join(previous, name)
		if err := os.Link(filepath.Join(destination, name), backup); err != nil {
			return err
		}
		if err := syncExportPath(backup); err != nil {
			return err
		}
		if i < len(contents) {
			path := filepath.Join(stage, name)
			if err := os.Chmod(path, info.Mode().Perm()); err != nil {
				return err
			}
			if err := syncExportPath(path); err != nil {
				return err
			}
		}
	}
	if err := syncExportPath(previous); err != nil {
		return err
	}
	journal.Phase = "prepared"
	cleanup = false
	if err := saveExportJournal(stage, journal); err != nil {
		return fmt.Errorf("cannot prepare export journal; recovery files retained in %s: %w", stage, err)
	}
	// Keep every backup intact, even during a partially successful rollback.
	rollback := func(cause error) error {
		if err := restoreExportJournal(destination, stage, journal, rename); err != nil {
			return errors.Join(cause, fmt.Errorf("rollback failed; recovery files retained in %s: %w", stage, err))
		}
		cleanup = true
		return cause
	}
	for i, name := range filenames {
		target := filepath.Join(destination, name)
		if i < len(contents) {
			err = rename(filepath.Join(stage, name), target)
		} else if existing[i] != nil {
			err = rename(target, filepath.Join(stage, "removed-"+name))
		} else {
			continue
		}
		if err != nil {
			return rollback(err)
		}
	}
	if err := syncExportPath(destination); err != nil {
		return rollback(err)
	}
	journal.Phase = "committed"
	// If the marker update fails, leave the complete output and journal for recovery.
	// Rolling back here could conflict with an already installed committed marker.
	if err := saveExportJournal(stage, journal); err != nil {
		return fmt.Errorf("cannot finalize export journal; recovery files retained in %s: %w", stage, err)
	}
	cleanup = true
	return nil
}
