package main

import (
	"archive/zip"
	"io"
	"testing"
)

func assertZipEntries(t *testing.T, path string, want map[string]string) {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got := map[string]string{}
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			continue
		}
		entry, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(entry)
		entry.Close()
		if err != nil {
			t.Fatal(err)
		}
		got[file.Name] = string(content)
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for name, content := range want {
		if got[name] != content {
			t.Fatalf("entries = %v, want %v", got, want)
		}
	}
}
