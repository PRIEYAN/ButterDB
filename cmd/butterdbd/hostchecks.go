package main

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/platform/config"
	"github.com/PRIEYAN/ButterDB/internal/platform/datadir"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

const (
	serverKeyBytes = 32
	stateFileName  = "state.json"

	magicNFS     = 0x6969
	magicSMB     = 0x517B
	magicCIFS    = 0xFF534D42
	magicFUSE    = 0x65735546
	dirSpool     = "spool"
	dirMultipart = "multipart"
)

type hostChecker struct {
	files   hostFiles
	volumes hostVolumes
	fsys    fsx.FS
	policy  secretPolicy
}

func newHostChecker(a *application) hostChecker {
	return hostChecker{
		files:   a.files,
		volumes: a.volumes,
		fsys:    a.fsys,
		policy:  secretPolicy{files: a.files, ids: a.ids},
	}
}

func (h hostChecker) run(cfg config.Config) []config.Finding {
	var found []config.Finding
	found = append(found, h.dataDir(cfg.DataDir)...)
	found = append(found, h.localFilesystem("data_dir", cfg.DataDir)...)
	if cacheDirInUse(cfg) {
		found = append(found, h.localFilesystem("cache.dir", cfg.Cache.Dir)...)
	}
	found = append(found, h.sameFilesystem(cfg)...)
	found = append(found, h.freeSpace(cfg)...)
	found = append(found, h.serverKey(cfg)...)
	found = append(found, h.operatorSecrets(cfg)...)
	found = append(found, h.tlsPairs(cfg)...)
	found = append(found, h.mediaFiles(cfg.Media)...)
	return found
}

func cacheDirInUse(cfg config.Config) bool {
	return cfg.Cache.BlocksMaxBytes > 0 || cfg.Media.Enabled
}

func (h hostChecker) dataDir(path string) []config.Finding {
	_, err := h.fsys.Stat(path)
	switch {
	case err == nil:
		if _, openErr := datadir.Open(h.fsys, path); openErr != nil {
			return []config.Finding{errorFinding("data_dir", describeDataDirError(openErr))}
		}
		return nil
	case errors.Is(err, fs.ErrNotExist):
		if !h.volumes.Writable(filepath.Dir(path)) {
			return []config.Finding{errorFinding("data_dir", path+": does not exist and its parent is not writable")}
		}
		return nil
	default:
		return []config.Finding{errorFinding("data_dir", path+": cannot inspect: "+describeFSError(err))}
	}
}

func describeDataDirError(err error) string {
	switch {
	case errors.Is(err, datadir.ErrLooseMode):
		return "mode grants group or other access; it must be 0700"
	case errors.Is(err, datadir.ErrForeignOwner):
		return "not owned by the process user"
	case errors.Is(err, datadir.ErrNotDirectory):
		return "not a directory"
	default:
		return "cannot be opened: " + describeFSError(errors.Unwrap(err))
	}
}

func (h hostChecker) nearestExisting(path string) string {
	for current := path; ; current = filepath.Dir(current) {
		if _, err := h.fsys.Stat(current); err == nil {
			return current
		}
		if filepath.Dir(current) == current {
			return path
		}
	}
}

func (h hostChecker) localFilesystem(key, path string) []config.Finding {
	magic, ok := h.volumes.FilesystemType(h.nearestExisting(path))
	if !ok {
		return nil
	}
	switch magic {
	case magicNFS:
		return []config.Finding{errorFinding(key, path+": on NFS; a local filesystem is required")}
	case magicSMB, magicCIFS:
		return []config.Finding{errorFinding(key, path+": on SMB or CIFS; a local filesystem is required")}
	case magicFUSE:
		return []config.Finding{errorFinding(key, path+": on a FUSE mount; a local filesystem is required")}
	}
	return nil
}

func (h hostChecker) sameFilesystem(cfg config.Config) []config.Finding {
	var found []config.Finding
	for _, name := range []string{dirSpool, dirMultipart} {
		path := filepath.Join(cfg.DataDir, name)
		same, known := h.volumes.SameDevice(cfg.DataDir, path)
		if known && !same {
			found = append(found, errorFinding("data_dir", path+": is on a different filesystem than data_dir"))
		}
	}
	if !cfg.Snapshot.UploadToTelegram {
		same, known := h.volumes.SameDevice(h.nearestExisting(cfg.DataDir), h.nearestExisting(cfg.Snapshot.Dir))
		if known && same {
			found = append(found, warningFinding("snapshot.dir", "on the same filesystem as data_dir; there is no off-host copy of control.db"))
		}
	}
	return found
}

func (h hostChecker) freeSpace(cfg config.Config) []config.Finding {
	free, ok := h.volumes.FreeBytes(h.nearestExisting(cfg.DataDir))
	if !ok {
		return nil
	}
	need := uint64(cfg.Spool.MaxBytes) + uint64(cfg.Spool.MinFreeBytes)
	if free < need {
		return []config.Finding{warningFinding("spool.max_bytes", fmt.Sprintf("data filesystem has %d bytes free; spool.max_bytes plus spool.min_free_bytes needs %d", free, need))}
	}
	return nil
}

func (h hostChecker) serverKey(cfg config.Config) []config.Finding {
	const key = "server_key_file"
	file, found := h.policy.inspect(key, cfg.ServerKeyFile)
	if !file.Exists {
		if len(found) > 0 {
			return found
		}
		return []config.Finding{h.missingServerKey(cfg)}
	}
	if file.Meta.Mode.IsRegular() && file.Meta.Size != serverKeyBytes {
		found = append(found, errorFinding(key, fmt.Sprintf("%s: holds %d bytes; the key file must hold exactly %d", file.Path, file.Meta.Size, serverKeyBytes)))
	}
	return found
}

func (h hostChecker) missingServerKey(cfg config.Config) config.Finding {
	if _, err := h.files.Lstat(filepath.Join(cfg.DataDir, stateFileName)); err == nil {
		return errorFinding("server_key_file", cfg.ServerKeyFile+": does not exist, but state.json shows an existing deployment")
	}
	return warningFinding("server_key_file", cfg.ServerKeyFile+": does not exist; serve generates it on first start")
}

func (h hostChecker) operatorSecrets(cfg config.Config) []config.Finding {
	var found []config.Finding
	required := []struct {
		key  string
		path string
		when bool
	}{
		{"s3.tls_key_file", cfg.S3.TLSKeyFile, cfg.S3.TLSMode == "files"},
		{"admin.tls_key_file", cfg.Admin.TLSKeyFile, cfg.Admin.TLSMode == "files"},
		{"ops.metrics_token_file", cfg.Ops.MetricsTokenFile, true},
	}
	for _, item := range required {
		if !item.when || item.path == "" {
			continue
		}
		file, findings := h.policy.inspect(item.key, item.path)
		found = append(found, findings...)
		if !file.Exists && len(findings) == 0 {
			found = append(found, errorFinding(item.key, item.path+": does not exist"))
		}
	}
	for _, item := range []struct{ key, path string }{
		{"telegram.app_file", cfg.Telegram.AppFile},
		{"telegram.session_file", cfg.Telegram.SessionFile},
		{"telegram.replica_session_file", cfg.Telegram.ReplicaSessionFile},
	} {
		_, findings := h.policy.inspect(item.key, item.path)
		found = append(found, findings...)
	}
	return found
}

func (h hostChecker) tlsPairs(cfg config.Config) []config.Finding {
	var found []config.Finding
	if cfg.S3.TLSMode == "files" && cfg.S3.TLSCertFile != "" && cfg.S3.TLSKeyFile != "" {
		found = append(found, h.pairFinding("s3.tls_cert_file", cfg.S3.TLSCertFile, cfg.S3.TLSKeyFile)...)
	}
	if cfg.Admin.TLSMode == "files" && cfg.Admin.TLSCertFile != "" && cfg.Admin.TLSKeyFile != "" {
		found = append(found, h.pairFinding("admin.tls_cert_file", cfg.Admin.TLSCertFile, cfg.Admin.TLSKeyFile)...)
	}
	return found
}

func (h hostChecker) pairFinding(key, certPath, keyPath string) []config.Finding {
	certPEM, err := h.files.ReadFile(certPath)
	if err != nil {
		return []config.Finding{errorFinding(key, certPath+": cannot read: "+describeFSError(err))}
	}
	keyPEM, err := h.files.ReadFile(keyPath)
	if err != nil {
		return []config.Finding{errorFinding(key, keyPath+": cannot read: "+describeFSError(err))}
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return []config.Finding{errorFinding(key, "certificate and key do not form a valid pair")}
	}
	return nil
}

func (h hostChecker) mediaFiles(cfg config.MediaConfig) []config.Finding {
	if !cfg.Enabled {
		return nil
	}
	var found []config.Finding
	for _, item := range []struct{ key, path string }{
		{"media.signing_key_file", cfg.SigningKeyFile},
		{"media.signing_key_previous_file", cfg.SigningKeyPreviousFile},
	} {
		if item.path == "" {
			continue
		}
		found = append(found, h.signingKey(item.key, item.path)...)
	}
	if cfg.WorkerMode == "spawn" {
		found = append(found, h.executable("media.worker_path", cfg.WorkerPath)...)
		found = append(found, h.executable("media.ffmpeg_path", cfg.FFmpegPath)...)
	}
	return found
}

func (h hostChecker) signingKey(key, path string) []config.Finding {
	file, found := h.policy.inspect(key, path)
	if !file.Exists {
		return found
	}
	content, err := h.files.ReadFile(file.Path)
	if err != nil {
		return append(found, errorFinding(key, file.Path+": cannot read: "+describeFSError(err)))
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(content)))
	if err != nil || len(decoded) != serverKeyBytes {
		found = append(found, errorFinding(key, file.Path+": does not decode to exactly 32 bytes"))
	}
	return found
}

func (h hostChecker) executable(key, name string) []config.Finding {
	path := name
	if !strings.ContainsRune(name, filepath.Separator) {
		resolved, err := h.files.LookPath(name)
		if err != nil {
			return []config.Finding{errorFinding(key, name+": not found")}
		}
		path = resolved
	}
	meta, err := h.files.Lstat(path)
	if err != nil {
		return []config.Finding{errorFinding(key, path+": cannot inspect: "+describeFSError(err))}
	}
	if meta.isSymlink() {
		resolved, _, resolveErr := h.policy.resolve(path)
		if resolveErr != nil {
			return []config.Finding{errorFinding(key, path+": cannot resolve symlink")}
		}
		path = resolved
		meta, _ = h.files.Lstat(path)
	}
	if !meta.Mode.IsRegular() || meta.Mode.Perm()&0o111 == 0 {
		return []config.Finding{errorFinding(key, path+": not an executable regular file")}
	}
	return nil
}
