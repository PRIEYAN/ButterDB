package main

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func copyFile(destination string, source string) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("release: open %s: %w", filepath.Base(source), err)
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("release: stat %s: %w", filepath.Base(source), err)
	}
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("release: create %s: %w", filepath.Base(destination), err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("release: copy %s: %w", filepath.Base(source), err)
	}
	return out.Close()
}

func moveFile(destination string, source string) error {
	if err := copyFile(destination, source); err != nil {
		return err
	}
	return os.Remove(source)
}

func zipSingleFile(destination string, source string, entryName string) (err error) {
	archive, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("release: create %s: %w", filepath.Base(destination), err)
	}
	writer := zip.NewWriter(archive)
	defer func() {
		err = closeArchive(writer, archive, err)
	}()
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("release: stat %s: %w", filepath.Base(source), err)
	}
	return addZipEntry(writer, source, entryName, info)
}

func zipDirectory(destination string, source string) (err error) {
	archive, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("release: create %s: %w", filepath.Base(destination), err)
	}
	writer := zip.NewWriter(archive)
	defer func() {
		err = closeArchive(writer, archive, err)
	}()
	root := filepath.Base(source)
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		return addZipEntry(writer, path, filepath.ToSlash(filepath.Join(root, relative)), info)
	})
}

func addZipEntry(writer *zip.Writer, path string, entryName string, info fs.FileInfo) error {
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return fmt.Errorf("release: zip header %s: %w", entryName, err)
	}
	header.Name = entryName
	if info.IsDir() {
		header.Name += "/"
		_, err := writer.CreateHeader(header)
		return err
	}
	header.Method = zip.Deflate
	entry, err := writer.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("release: zip entry %s: %w", entryName, err)
	}
	source, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("release: open %s: %w", entryName, err)
	}
	defer source.Close()
	if _, err := io.Copy(entry, source); err != nil {
		return fmt.Errorf("release: zip copy %s: %w", entryName, err)
	}
	return nil
}

func closeArchive(writer *zip.Writer, archive *os.File, previous error) error {
	closeErr := writer.Close()
	fileErr := archive.Close()
	if previous != nil {
		return previous
	}
	if closeErr != nil {
		return fmt.Errorf("release: finish archive: %w", closeErr)
	}
	return fileErr
}
