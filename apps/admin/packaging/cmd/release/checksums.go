package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("release: open artifact: %w", err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", fmt.Errorf("release: hash artifact: %w", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func mergeChecksumLines(existing string, name string, sum string) string {
	lines := map[string]string{}
	for _, line := range strings.Split(existing, "\n") {
		recordedSum, recordedName, ok := strings.Cut(line, "  ")
		if ok && recordedName != name {
			lines[recordedName] = recordedSum
		}
	}
	lines[name] = sum
	names := make([]string, 0, len(lines))
	for recordedName := range lines {
		names = append(names, recordedName)
	}
	sort.Strings(names)
	var merged strings.Builder
	for _, recordedName := range names {
		merged.WriteString(lines[recordedName] + "  " + recordedName + "\n")
	}
	return merged.String()
}

func recordChecksum(distDir string, name string) error {
	sum, err := fileSHA256(filepath.Join(distDir, name))
	if err != nil {
		return err
	}
	path := filepath.Join(distDir, checksumsFile)
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("release: read %s: %w", checksumsFile, err)
	}
	return replaceFile(path, []byte(mergeChecksumLines(string(existing), name, sum)))
}

func replaceFile(path string, content []byte) error {
	temp := path + ".tmp"
	if err := os.WriteFile(temp, content, 0o644); err != nil {
		return fmt.Errorf("release: write %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("release: replace %s: %w", filepath.Base(path), err)
	}
	return nil
}
