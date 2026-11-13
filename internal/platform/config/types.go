package config

type Config struct {
	DataDir       string         `toml:"data_dir" kind:"path"`
	ServerKeyFile string         `toml:"server_key_file" kind:"path"`
	Log           LogConfig      `toml:"log"`
	S3            S3Config       `toml:"s3"`
	Ingest        IngestConfig   `toml:"ingest"`
	Admin         AdminConfig    `toml:"admin"`
	Media         MediaConfig    `toml:"media"`
	Ops           OpsConfig      `toml:"ops"`
	Telegram      TelegramConfig `toml:"telegram"`
	Cache         CacheConfig    `toml:"cache"`
	Spool         SpoolConfig    `toml:"spool"`
	Snapshot      SnapshotConfig `toml:"snapshot"`
	Shutdown      ShutdownConfig `toml:"shutdown"`
}

type LogConfig struct {
	Level      string `toml:"level" enum:"debug,info,warn,error"`
	Format     string `toml:"format" enum:"json,text"`
	AccessLog  bool   `toml:"access_log"`
	ObjectKeys string `toml:"object_keys" enum:"full,hash,none"`
}

type S3Config struct {
	Listen                   string   `toml:"listen" kind:"address"`
	PublicURL                string   `toml:"public_url"`
	Region                   string   `toml:"region"`
	BaseDomains              []string `toml:"base_domains"`
	TLSMode                  string   `toml:"tls_mode" enum:"none,files"`
	TLSCertFile              string   `toml:"tls_cert_file" kind:"path" optional:"true"`
	TLSKeyFile               string   `toml:"tls_key_file" kind:"path" optional:"true"`
	TrustedProxies           []string `toml:"trusted_proxies"`
	RequestBudget            Duration `toml:"request_budget"`
	BodyIdleTimeout          Duration `toml:"body_idle_timeout"`
	WriteIdleTimeout         Duration `toml:"write_idle_timeout"`
	KeepaliveInterval        Duration `toml:"keepalive_interval"`
	CompletedUploadRetention Duration `toml:"completed_upload_retention"`
	MultipartIdleExpiry      Duration `toml:"multipart_idle_expiry"`
	AuthFailureLimit         int      `toml:"auth_failure_limit" min:"1"`
	CORSAllowedOrigins       []string `toml:"cors_allowed_origins"`
	CORSAllowedMethods       []string `toml:"cors_allowed_methods"`
	CORSAllowedHeaders       []string `toml:"cors_allowed_headers"`
	CORSExposeHeaders        []string `toml:"cors_expose_headers"`
	CORSMaxAge               int      `toml:"cors_max_age" min:"0"`
	AnonymousRate            int      `toml:"anonymous_rate" min:"1" max:"10000"`
	AnonymousBurst           int      `toml:"anonymous_burst" min:"1" max:"10000"`
}

type IngestConfig struct {
	MaxConcurrentPuts       int `toml:"max_concurrent_puts" min:"1" max:"64"`
	MaxConcurrentPutsPerKey int `toml:"max_concurrent_puts_per_key" min:"1" max:"64"`
}

type AdminConfig struct {
	Listen             string   `toml:"listen" kind:"address"`
	TLSMode            string   `toml:"tls_mode" enum:"self-signed,files,proxy"`
	TLSCertFile        string   `toml:"tls_cert_file" kind:"path" optional:"true"`
	TLSKeyFile         string   `toml:"tls_key_file" kind:"path" optional:"true"`
	Hostnames          []string `toml:"hostnames"`
	TrustedProxies     []string `toml:"trusted_proxies"`
	ControlSocket      string   `toml:"control_socket" kind:"path"`
	SetupTokenTTL      Duration `toml:"setup_token_ttl"`
	PairingCodeTTL     Duration `toml:"pairing_code_ttl"`
	DeviceIdleRevoke   Duration `toml:"device_idle_revoke"`
	UploadCommitBudget Duration `toml:"upload_commit_budget"`
}

type MediaConfig struct {
	Enabled                bool     `toml:"enabled"`
	Listen                 string   `toml:"listen" kind:"address"`
	PathPrefix             string   `toml:"path_prefix"`
	PublicURL              string   `toml:"public_url"`
	Hostnames              []string `toml:"hostnames"`
	TrustedProxies         []string `toml:"trusted_proxies"`
	SigningKeyFile         string   `toml:"signing_key_file" kind:"path"`
	SigningKeyPreviousFile string   `toml:"signing_key_previous_file" kind:"path" optional:"true"`
	PrivateMaxExpiry       Duration `toml:"private_max_expiry"`
	WidthLadder            []int    `toml:"width_ladder"`
	WorkerMode             string   `toml:"worker_mode" enum:"spawn,external"`
	WorkerPath             string   `toml:"worker_path"`
	WorkerSocket           string   `toml:"worker_socket" kind:"path"`
	WorkerTmpDir           string   `toml:"worker_tmp_dir" kind:"path"`
	FFmpegPath             string   `toml:"ffmpeg_path" kind:"path"`
	WorkerConcurrency      int      `toml:"worker_concurrency" min:"1"`
	WorkerMemoryLimit      ByteSize `toml:"worker_memory_limit"`
	WorkerSandbox          string   `toml:"worker_sandbox" enum:"auto,require,off"`
	QueueDepth             int      `toml:"queue_depth" min:"1"`
	QueueTimeout           Duration `toml:"queue_timeout"`
	SourceTimeout          Duration `toml:"source_timeout"`
	ImageTimeout           Duration `toml:"image_timeout"`
	PosterTimeout          Duration `toml:"poster_timeout"`
	PoisonTTL              Duration `toml:"poison_ttl"`
	NegativeTTL            Duration `toml:"negative_ttl"`
	CacheMaxBytes          ByteSize `toml:"cache_max_bytes"`
	CacheMemoryMaxBytes    ByteSize `toml:"cache_memory_max_bytes"`
	CacheSweepInterval     Duration `toml:"cache_sweep_interval"`
	MaxImageSourceBytes    ByteSize `toml:"max_image_source_bytes"`
	MaxVideoSourceBytes    ByteSize `toml:"max_video_source_bytes"`
	MaxSourcePixels        int      `toml:"max_source_pixels" min:"1"`
	MaxSourceSide          int      `toml:"max_source_side" min:"1"`
	MaxOutputSide          int      `toml:"max_output_side" min:"1"`
	MaxOutputPixels        int      `toml:"max_output_pixels" min:"1"`
	MaxOutputBytes         ByteSize `toml:"max_output_bytes"`
	MaxFrames              int      `toml:"max_frames" min:"1"`
	MaxAnimatedPixels      int      `toml:"max_animated_pixels" min:"1"`
	AVIFAutoMaxPixels      int      `toml:"avif_auto_max_pixels" min:"1"`
}

type OpsConfig struct {
	Listen                      string   `toml:"listen" kind:"address"`
	Hostnames                   []string `toml:"hostnames"`
	MetricsTokenFile            string   `toml:"metrics_token_file" kind:"path" optional:"true"`
	AllowUnauthenticatedMetrics bool     `toml:"allow_unauthenticated_metrics"`
	ReadyWhenDegraded           bool     `toml:"ready_when_degraded"`
}

type TelegramConfig struct {
	AppFile            string         `toml:"app_file" kind:"path"`
	SessionFile        string         `toml:"session_file" kind:"path"`
	ReplicaSessionFile string         `toml:"replica_session_file" kind:"path"`
	ConnectTimeout     Duration       `toml:"connect_timeout"`
	DeviceModel        string         `toml:"device_model"`
	TestDC             bool           `toml:"test_dc"`
	SendRate           SendRateConfig `toml:"send_rate"`
}

type SendRateConfig struct {
	ControlPerMinute   int `toml:"control_per_minute" min:"1" max:"600"`
	ControlBurst       int `toml:"control_burst" min:"1" max:"100"`
	DocumentsPerMinute int `toml:"documents_per_minute" min:"1" max:"120"`
	DocumentsBurst     int `toml:"documents_burst" min:"1" max:"20"`
}

type CacheConfig struct {
	Dir               string   `toml:"dir" kind:"path"`
	BlocksMaxBytes    ByteSize `toml:"blocks_max_bytes"`
	WarmOnPutMaxBytes ByteSize `toml:"warm_on_put_max_bytes" min:"0"`
	MinFreeBytes      ByteSize `toml:"min_free_bytes" min:"0"`
}

type SpoolConfig struct {
	MaxBytes          ByteSize `toml:"max_bytes"`
	MultipartMaxBytes ByteSize `toml:"multipart_max_bytes"`
	MinFreeBytes      ByteSize `toml:"min_free_bytes" min:"0"`
}

type SnapshotConfig struct {
	Dir              string   `toml:"dir" kind:"path"`
	Interval         Duration `toml:"interval" min:"0s"`
	Retain           int      `toml:"retain" min:"1" max:"365"`
	UploadToTelegram bool     `toml:"upload_to_telegram"`
	TelegramRetain   int      `toml:"telegram_retain" min:"1" max:"365"`
	BeforeMigrate    bool     `toml:"before_migrate"`
}

type ShutdownConfig struct {
	DrainTimeout     Duration `toml:"drain_timeout"`
	ComponentTimeout Duration `toml:"component_timeout"`
	TotalTimeout     Duration `toml:"total_timeout"`
}

type LoadOptions struct {
	Path         string
	PathExplicit bool
	Environ      []string
}

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

type Finding struct {
	Severity Severity
	Key      string
	Message  string
}
