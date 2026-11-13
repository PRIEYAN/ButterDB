package config

import (
	"runtime"
	"strings"
	"time"
)

const (
	defaultDataDir     = "/var/lib/butterdb"
	dataDirToken       = "<data_dir>"
	credentialsDirRef  = "${CREDENTIALS_DIRECTORY}"
	credentialsDirName = "CREDENTIALS_DIRECTORY"
	maxDefaultWorkers  = 4
)

func Default() Config {
	cfg := tokenDefaults()
	tokens := tokenDefaults()
	expandDefaultTokens(&cfg, &tokens, defaultDataDir)
	return cfg
}

func tokenDefaults() Config {
	return Config{
		DataDir:       defaultDataDir,
		ServerKeyFile: "/etc/butterdb/server.key",
		Log: LogConfig{
			Level:      "info",
			Format:     "json",
			AccessLog:  true,
			ObjectKeys: "full",
		},
		S3: S3Config{
			Listen:                   "0.0.0.0:9000",
			Region:                   "us-east-1",
			BaseDomains:              []string{},
			TLSMode:                  "none",
			TrustedProxies:           loopbackProxies(),
			RequestBudget:            Duration(30 * time.Second),
			BodyIdleTimeout:          Duration(60 * time.Second),
			WriteIdleTimeout:         Duration(60 * time.Second),
			KeepaliveInterval:        Duration(15 * time.Second),
			CompletedUploadRetention: Duration(24 * time.Hour),
			MultipartIdleExpiry:      Duration(168 * time.Hour),
			AuthFailureLimit:         100,
			CORSAllowedOrigins:       []string{},
			CORSAllowedMethods:       []string{"GET", "HEAD", "PUT", "POST", "DELETE"},
			CORSAllowedHeaders:       []string{"*"},
			CORSExposeHeaders:        defaultExposeHeaders(),
			CORSMaxAge:               3600,
			AnonymousRate:            50,
			AnonymousBurst:           100,
		},
		Ingest: IngestConfig{
			MaxConcurrentPuts:       4,
			MaxConcurrentPutsPerKey: 2,
		},
		Admin: AdminConfig{
			Listen:             "0.0.0.0:9443",
			TLSMode:            "self-signed",
			Hostnames:          []string{},
			TrustedProxies:     loopbackProxies(),
			ControlSocket:      dataDirToken + "/admin.sock",
			SetupTokenTTL:      Duration(24 * time.Hour),
			PairingCodeTTL:     Duration(10 * time.Minute),
			DeviceIdleRevoke:   Duration(4320 * time.Hour),
			UploadCommitBudget: Duration(10 * time.Minute),
		},
		Media: mediaDefaults(),
		Ops: OpsConfig{
			Listen:            "127.0.0.1:9090",
			Hostnames:         []string{},
			ReadyWhenDegraded: true,
		},
		Telegram: TelegramConfig{
			AppFile:            dataDirToken + "/secrets/telegram_app.json",
			SessionFile:        dataDirToken + "/secrets/session_primary.json",
			ReplicaSessionFile: dataDirToken + "/secrets/session_replica.json",
			ConnectTimeout:     Duration(30 * time.Second),
			DeviceModel:        "butterDB server",
			SendRate: SendRateConfig{
				ControlPerMinute:   20,
				ControlBurst:       5,
				DocumentsPerMinute: 6,
				DocumentsBurst:     1,
			},
		},
		Cache: CacheConfig{
			Dir:               dataDirToken + "/cache",
			BlocksMaxBytes:    20 << 30,
			WarmOnPutMaxBytes: 64 << 20,
			MinFreeBytes:      2 << 30,
		},
		Spool: SpoolConfig{
			MaxBytes:          8 << 30,
			MultipartMaxBytes: 16 << 30,
			MinFreeBytes:      1 << 30,
		},
		Snapshot: SnapshotConfig{
			Dir:            dataDirToken + "/snapshots",
			Interval:       Duration(24 * time.Hour),
			Retain:         7,
			TelegramRetain: 7,
			BeforeMigrate:  true,
		},
		Shutdown: ShutdownConfig{
			DrainTimeout:     Duration(10 * time.Second),
			ComponentTimeout: Duration(5 * time.Second),
			TotalTimeout:     Duration(60 * time.Second),
		},
	}
}

func mediaDefaults() MediaConfig {
	return MediaConfig{
		Listen:              "0.0.0.0:9080",
		Hostnames:           []string{},
		TrustedProxies:      loopbackProxies(),
		SigningKeyFile:      dataDirToken + "/secrets/media_signing.key",
		PrivateMaxExpiry:    Duration(168 * time.Hour),
		WidthLadder:         defaultWidthLadder(),
		WorkerMode:          "spawn",
		WorkerPath:          "butterdb-transformd",
		WorkerSocket:        dataDirToken + "/run/transformd.sock",
		WorkerTmpDir:        dataDirToken + "/run/transformd-tmp",
		FFmpegPath:          "/usr/bin/ffmpeg",
		WorkerConcurrency:   min(runtime.NumCPU(), maxDefaultWorkers),
		WorkerMemoryLimit:   1 << 30,
		WorkerSandbox:       "auto",
		QueueDepth:          64,
		QueueTimeout:        Duration(10 * time.Second),
		SourceTimeout:       Duration(120 * time.Second),
		ImageTimeout:        Duration(20 * time.Second),
		PosterTimeout:       Duration(30 * time.Second),
		PoisonTTL:           Duration(time.Hour),
		NegativeTTL:         Duration(10 * time.Minute),
		CacheMaxBytes:       10 << 30,
		CacheMemoryMaxBytes: 128 << 20,
		CacheSweepInterval:  Duration(time.Hour),
		MaxImageSourceBytes: 64 << 20,
		MaxVideoSourceBytes: 1 << 30,
		MaxSourcePixels:     50000000,
		MaxSourceSide:       32768,
		MaxOutputSide:       4096,
		MaxOutputPixels:     16777216,
		MaxOutputBytes:      32 << 20,
		MaxFrames:           200,
		MaxAnimatedPixels:   200000000,
		AVIFAutoMaxPixels:   4194304,
	}
}

func loopbackProxies() []string {
	return []string{"127.0.0.1/32", "::1/128"}
}

func defaultExposeHeaders() []string {
	return []string{
		"ETag", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified",
		"Content-Type", "Content-Disposition", "Content-Encoding", "Cache-Control",
		"Expires", "x-amz-request-id", "x-amz-id-2", "x-amz-server-side-encryption",
		"x-amz-checksum-crc32", "x-amz-checksum-crc32c", "x-amz-checksum-crc64nvme",
		"x-amz-checksum-sha1", "x-amz-checksum-sha256",
	}
}

func defaultWidthLadder() []int {
	return []int{
		16, 32, 48, 64, 80, 96, 128, 160, 192, 240, 256, 320, 384, 400, 480, 512, 576, 640, 720, 768,
		800, 960, 1024, 1080, 1200, 1280, 1440, 1536, 1600, 1920, 2048, 2400, 2560, 2880, 3072,
		3200, 3840, 4096,
	}
}

func expandDefaultTokens(cfg *Config, defaults *Config, dataDir string) {
	current := walkFields(cfg)
	original := walkFields(defaults)
	for i, f := range current {
		if f.kind != kindPath {
			continue
		}
		value := f.value.String()
		if value != original[i].value.String() || !strings.HasPrefix(value, dataDirToken) {
			continue
		}
		f.value.SetString(dataDir + strings.TrimPrefix(value, dataDirToken))
	}
}
