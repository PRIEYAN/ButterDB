package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/PRIEYAN/ButterDB/internal/platform/config"
)

const (
	groupReadBit  fs.FileMode = 0o040
	groupWriteExe fs.FileMode = 0o030
	otherBits     fs.FileMode = 0o007
)

type secretFile struct {
	Path   string
	Meta   fileMeta
	Exists bool
}

type secretPolicy struct {
	files hostFiles
	ids   identity
}

func (p secretPolicy) resolve(path string) (string, fileMeta, error) {
	meta, err := p.files.Lstat(path)
	if err != nil {
		return path, fileMeta{}, err
	}
	if !meta.isSymlink() {
		return path, meta, nil
	}
	target, err := p.files.Readlink(path)
	if err != nil {
		return path, fileMeta{}, err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	targetMeta, err := p.files.Lstat(target)
	if err != nil {
		return target, fileMeta{}, err
	}
	if targetMeta.isSymlink() {
		return target, fileMeta{}, errors.New("symlink target is itself a symlink")
	}
	return target, targetMeta, nil
}

func (p secretPolicy) inspect(key, path string) (secretFile, []config.Finding) {
	target, meta, err := p.resolve(path)
	if errors.Is(err, fs.ErrNotExist) {
		return secretFile{Path: path}, nil
	}
	if err != nil {
		return secretFile{Path: path}, []config.Finding{errorFinding(key, fmt.Sprintf("%s: cannot inspect: %s", path, describeFSError(err)))}
	}
	return secretFile{Path: target, Meta: meta, Exists: true}, p.permissionFindings(key, target, meta)
}

func (p secretPolicy) permissionFindings(key, path string, meta fileMeta) []config.Finding {
	var found []config.Finding
	if !meta.Mode.IsRegular() {
		return []config.Finding{errorFinding(key, path+": not a regular file")}
	}
	perm := meta.Mode.Perm()
	switch {
	case perm&otherBits != 0:
		found = append(found, errorFinding(key, fmt.Sprintf("%s: mode %04o allows group or other access", path, perm)))
	case perm&groupWriteExe != 0:
		found = append(found, errorFinding(key, fmt.Sprintf("%s: mode %04o allows group write or execute", path, perm)))
	case perm&groupReadBit != 0:
		found = append(found, p.groupReadFinding(key, path, perm, meta))
	}
	if meta.OwnerKnown && meta.UID != p.ids.UID && meta.UID != 0 {
		found = append(found, errorFinding(key, fmt.Sprintf("%s: owned by uid %d, which is neither the process user nor root", path, meta.UID)))
	}
	return found
}

// Kubernetes secret volumes with fsGroup cannot be 0600 for a non-root process, so a root-owned file in the process group is only a warning.
func (p secretPolicy) groupReadFinding(key, path string, perm fs.FileMode, meta fileMeta) config.Finding {
	if meta.OwnerKnown && meta.UID == 0 && meta.GID == p.ids.GID {
		return warningFinding(key, fmt.Sprintf("%s: mode %04o is group readable; accepted because the file is root-owned in the process group", path, perm))
	}
	return errorFinding(key, fmt.Sprintf("%s: mode %04o allows group read by a group other than the process group", path, perm))
}

func describeFSError(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}

func errorFinding(key, message string) config.Finding {
	return config.Finding{Severity: config.SeverityError, Key: key, Message: message}
}

func warningFinding(key, message string) config.Finding {
	return config.Finding{Severity: config.SeverityWarning, Key: key, Message: message}
}
