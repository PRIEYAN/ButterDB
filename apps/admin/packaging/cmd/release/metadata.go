package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const fyneMetadataFile = "FyneApp.toml"

type appMetadata struct {
	Name    string
	ID      string
	Version string
	Icon    string
	Build   int
}

func loadMetadata(appDir string) (appMetadata, error) {
	file, err := os.Open(filepath.Join(appDir, fyneMetadataFile))
	if err != nil {
		return appMetadata{}, fmt.Errorf("release: open %s: %w", fyneMetadataFile, err)
	}
	defer file.Close()
	meta, err := parseMetadata(bufio.NewScanner(file))
	if err != nil {
		return appMetadata{}, err
	}
	if err := meta.validate(); err != nil {
		return appMetadata{}, err
	}
	return meta, nil
}

func parseMetadata(scanner *bufio.Scanner) (appMetadata, error) {
	var meta appMetadata
	inDetails := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") {
			inDetails = line == "[Details]"
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !inDetails || !ok {
			continue
		}
		if err := meta.assign(strings.TrimSpace(key), strings.TrimSpace(value)); err != nil {
			return appMetadata{}, err
		}
	}
	if err := scanner.Err(); err != nil {
		return appMetadata{}, fmt.Errorf("release: read %s: %w", fyneMetadataFile, err)
	}
	return meta, nil
}

func (m *appMetadata) assign(key, value string) error {
	switch key {
	case "Name":
		m.Name = unquote(value)
	case "ID":
		m.ID = unquote(value)
	case "Version":
		m.Version = unquote(value)
	case "Icon":
		m.Icon = unquote(value)
	case "Build":
		build, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("release: %s Build is not an integer", fyneMetadataFile)
		}
		m.Build = build
	}
	return nil
}

func (m appMetadata) validate() error {
	if m.Name == "" || m.ID == "" {
		return fmt.Errorf("release: %s needs Details Name and ID", fyneMetadataFile)
	}
	return nil
}

func unquote(value string) string {
	unquoted, err := strconv.Unquote(value)
	if err != nil {
		return strings.Trim(value, `"'`)
	}
	return unquoted
}
