package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/fsx"
)

const testServerID = "srv-1"

var testNow = time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)

func testConfig(dir string, hostnames []string, now time.Time) Config {
	return Config{
		Dir:       dir,
		ServerID:  testServerID,
		Hostnames: hostnames,
		Now:       now,
		Random:    rand.Reader,
		Hostname:  func() (string, error) { return "build-host", nil },
		Files:     fsx.OS{},
	}
}

func mustLoad(t *testing.T, cfg Config) Material {
	t.Helper()
	material, err := LoadOrCreateWith(cfg)
	if err != nil {
		t.Fatalf("LoadOrCreateWith: %v", err)
	}
	return material
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func keyPath(dir string) string  { return filepath.Join(dir, "tls", "admin.key") }
func certPath(dir string) string { return filepath.Join(dir, "tls", "admin.crt") }

func TestLoadOrCreateGeneratesP256Key(t *testing.T) {
	dir := t.TempDir()
	material := mustLoad(t, testConfig(dir, []string{"files.example.test"}, testNow))

	block, rest := pem.Decode(readFile(t, keyPath(dir)))
	if block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
		t.Fatalf("key file is not a single PRIVATE KEY PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("ParsePKCS8PrivateKey: %v", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		t.Fatalf("key is %T, want ECDSA P-256", parsed)
	}

	leaf := material.Certificate.Leaf
	if leaf == nil {
		t.Fatal("leaf is nil")
	}
	if leaf.Subject.CommonName != "butterdbd admin "+testServerID {
		t.Fatalf("common name = %q", leaf.Subject.CommonName)
	}
	if want := testNow.Add(825 * 24 * time.Hour); !leaf.NotAfter.Equal(want) || !material.NotAfter.Equal(want) {
		t.Fatalf("NotAfter = %v, want %v", material.NotAfter, want)
	}
	if !leaf.PublicKey.(*ecdsa.PublicKey).Equal(&key.PublicKey) {
		t.Fatal("certificate public key does not match key file")
	}
	if want := sha256.Sum256(leaf.RawSubjectPublicKeyInfo); material.SPKISHA256 != want {
		t.Fatal("SPKI hash is not sha256 of the leaf SubjectPublicKeyInfo")
	}
	wantDNS := []string{"build-host", "files.example.test", "localhost"}
	gotDNS := slices.Clone(leaf.DNSNames)
	slices.Sort(gotDNS)
	if !slices.Equal(gotDNS, wantDNS) {
		t.Fatalf("DNS SANs = %v, want %v", gotDNS, wantDNS)
	}
	var gotIPs []string
	for _, ip := range leaf.IPAddresses {
		gotIPs = append(gotIPs, ip.String())
	}
	slices.Sort(gotIPs)
	if want := []string{"127.0.0.1", "::1"}; !slices.Equal(gotIPs, want) {
		t.Fatalf("IP SANs = %v, want %v", gotIPs, want)
	}
	if info, err := os.Stat(certPath(dir)); err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("certificate stat = %v, %v; want mode 0644", info, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "tls")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("tls directory stat = %v, %v; want mode 0700", info, err)
	}
}

func TestRenewalKeepsSPKI(t *testing.T) {
	tests := []struct {
		name        string
		elapsed     time.Duration
		wantReissue bool
	}{
		{"thirty one days remain keeps certificate", 794 * 24 * time.Hour, false},
		{"twenty nine days remain reissues", 796 * 24 * time.Hour, true},
		{"expired reissues", 900 * 24 * time.Hour, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			first := mustLoad(t, testConfig(dir, nil, testNow))
			keyBefore := readFile(t, keyPath(dir))

			later := testNow.Add(tc.elapsed)
			second := mustLoad(t, testConfig(dir, nil, later))

			if first.SPKISHA256 != second.SPKISHA256 {
				t.Fatal("SPKI fingerprint changed across renewal")
			}
			if string(keyBefore) != string(readFile(t, keyPath(dir))) {
				t.Fatal("key file changed across renewal")
			}
			reissued := !second.NotAfter.Equal(first.NotAfter)
			if reissued != tc.wantReissue {
				t.Fatalf("reissued = %v, want %v", reissued, tc.wantReissue)
			}
			if tc.wantReissue && !second.NotAfter.Equal(later.Add(825*24*time.Hour)) {
				t.Fatalf("renewed NotAfter = %v", second.NotAfter)
			}
		})
	}
}

func TestSANChangeReissues(t *testing.T) {
	tests := []struct {
		name      string
		before    []string
		after     []string
		wantReuse bool
		wantDNS   string
		wantIP    string
	}{
		{"added hostname reissues", nil, []string{"admin.example.test"}, false, "admin.example.test", ""},
		{"added address reissues", nil, []string{"10.1.2.3"}, false, "", "10.1.2.3"},
		{"removed hostname reissues", []string{"admin.example.test"}, nil, false, "", ""},
		{"same set in other order and case is stable", []string{"A.example.test", "b.example.test"}, []string{"b.example.test", "a.example.test"}, true, "", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			first := mustLoad(t, testConfig(dir, tc.before, testNow))
			second := mustLoad(t, testConfig(dir, tc.after, testNow.Add(time.Hour)))

			if first.SPKISHA256 != second.SPKISHA256 {
				t.Fatal("SPKI fingerprint changed when SAN set changed")
			}
			reused := string(first.Certificate.Certificate[0]) == string(second.Certificate.Certificate[0])
			if reused != tc.wantReuse {
				t.Fatalf("certificate reused = %v, want %v", reused, tc.wantReuse)
			}
			leaf := second.Certificate.Leaf
			if tc.wantDNS != "" && !slices.Contains(leaf.DNSNames, tc.wantDNS) {
				t.Fatalf("DNS SANs %v lack %s", leaf.DNSNames, tc.wantDNS)
			}
			if tc.wantIP != "" && leaf.VerifyHostname(tc.wantIP) != nil {
				t.Fatalf("certificate does not cover %s", tc.wantIP)
			}
			if !tc.wantReuse && tc.after == nil && slices.Contains(leaf.DNSNames, "admin.example.test") {
				t.Fatalf("removed SAN still present: %v", leaf.DNSNames)
			}
		})
	}
}

func TestKeyFileMode0600(t *testing.T) {
	dir := t.TempDir()
	mustLoad(t, testConfig(dir, nil, testNow))
	info, err := os.Stat(keyPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %04o, want 0600", info.Mode().Perm())
	}
	mustLoad(t, testConfig(dir, []string{"x.example.test"}, testNow))
	if info, _ = os.Stat(keyPath(dir)); info.Mode().Perm() != 0o600 {
		t.Fatalf("key mode after reissue = %04o, want 0600", info.Mode().Perm())
	}
}

func TestFingerprintFormats(t *testing.T) {
	var spki [32]byte
	for i := range spki {
		spki[i] = byte(i * 9)
	}
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"canonical", Fingerprint(spki), "SHA256:" + hex32(spki)},
		{"display", DisplayFingerprint(spki), groups32(spki)},
		{"zero canonical", Fingerprint([32]byte{}), "SHA256:" + strings.Repeat("0", 64)},
		{"zero display", DisplayFingerprint([32]byte{}), strings.TrimSpace(strings.Repeat("0000 ", 16))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
	canonical := Fingerprint(spki)
	if len(canonical) != len("SHA256:")+64 || canonical != "SHA256:"+strings.ToLower(canonical[7:]) {
		t.Fatalf("canonical form malformed: %q", canonical)
	}
	if got := len(strings.Fields(DisplayFingerprint(spki))); got != 16 {
		t.Fatalf("display groups = %d, want 16", got)
	}
}

func hex32(b [32]byte) string {
	const digits = "0123456789abcdef"
	var sb strings.Builder
	for _, v := range b {
		sb.WriteByte(digits[v>>4])
		sb.WriteByte(digits[v&0xf])
	}
	return sb.String()
}

func groups32(b [32]byte) string {
	h := hex32(b)
	var parts []string
	for i := 0; i < len(h); i += 4 {
		parts = append(parts, h[i:i+4])
	}
	return strings.Join(parts, " ")
}

func TestLoadExistingIsStable(t *testing.T) {
	dir := t.TempDir()
	first := mustLoad(t, testConfig(dir, []string{"h.example.test"}, testNow))
	keyBytes := readFile(t, keyPath(dir))
	certBytes := readFile(t, certPath(dir))

	second := mustLoad(t, testConfig(dir, []string{"h.example.test"}, testNow.Add(24*time.Hour)))

	if first.SPKISHA256 != second.SPKISHA256 || !first.NotAfter.Equal(second.NotAfter) {
		t.Fatal("material changed on plain reload")
	}
	if string(first.Certificate.Certificate[0]) != string(second.Certificate.Certificate[0]) {
		t.Fatal("certificate was reissued on plain reload")
	}
	if string(keyBytes) != string(readFile(t, keyPath(dir))) || string(certBytes) != string(readFile(t, certPath(dir))) {
		t.Fatal("files were rewritten on plain reload")
	}
}

func TestRejectsGroupReadableKey(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o660, 0o644} {
		t.Run(mode.String(), func(t *testing.T) {
			dir := t.TempDir()
			mustLoad(t, testConfig(dir, nil, testNow))
			if err := os.Chmod(keyPath(dir), mode); err != nil {
				t.Fatal(err)
			}
			_, err := LoadOrCreateWith(testConfig(dir, nil, testNow))
			if !errors.Is(err, ErrKeyFilePermissions) {
				t.Fatalf("error = %v, want ErrKeyFilePermissions", err)
			}
		})
	}
}

func TestRejectsMalformedKeyFile(t *testing.T) {
	other, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(other)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		body []byte
	}{
		{"garbage", []byte("not pem")},
		{"wrong pem type", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1, 2, 3}})},
		{"p384 key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "tls"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(keyPath(dir), tc.body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadOrCreateWith(testConfig(dir, nil, testNow)); !errors.Is(err, ErrInvalidKeyFile) {
				t.Fatalf("error = %v, want ErrInvalidKeyFile", err)
			}
			if string(readFile(t, keyPath(dir))) != string(tc.body) {
				t.Fatal("malformed key file was overwritten")
			}
		})
	}
}

func TestReissuesWhenCertificateIsUnusable(t *testing.T) {
	tests := []struct {
		name   string
		break_ func(t *testing.T, dir string)
	}{
		{"missing certificate", func(t *testing.T, dir string) {
			if err := os.Remove(certPath(dir)); err != nil {
				t.Fatal(err)
			}
		}},
		{"garbage certificate", func(t *testing.T, dir string) {
			if err := os.WriteFile(certPath(dir), []byte("junk"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			first := mustLoad(t, testConfig(dir, nil, testNow))
			tc.break_(t, dir)
			second := mustLoad(t, testConfig(dir, nil, testNow))
			if first.SPKISHA256 != second.SPKISHA256 {
				t.Fatal("SPKI changed when only the certificate was lost")
			}
			if info, err := os.Stat(certPath(dir)); err != nil || info.Mode().Perm() != 0o644 {
				t.Fatalf("certificate stat = %v, %v", info, err)
			}
		})
	}
}

func TestDeletingKeyChangesSPKI(t *testing.T) {
	dir := t.TempDir()
	first := mustLoad(t, testConfig(dir, nil, testNow))
	if err := os.Remove(keyPath(dir)); err != nil {
		t.Fatal(err)
	}
	second := mustLoad(t, testConfig(dir, nil, testNow))
	if first.SPKISHA256 == second.SPKISHA256 {
		t.Fatal("SPKI unchanged after the key was deleted")
	}
}

func TestHostnameErrorIsTolerated(t *testing.T) {
	cfg := testConfig(t.TempDir(), nil, testNow)
	cfg.Hostname = func() (string, error) { return "", errors.New("no hostname") }
	material := mustLoad(t, cfg)
	if !slices.Equal(material.Certificate.Leaf.DNSNames, []string{"localhost"}) {
		t.Fatalf("DNS SANs = %v", material.Certificate.Leaf.DNSNames)
	}
}

func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr error
	}{
		{"nil random", func(c *Config) { c.Random = nil }, errs.ErrMissingDependency},
		{"nil hostname", func(c *Config) { c.Hostname = nil }, errs.ErrMissingDependency},
		{"nil files", func(c *Config) { c.Files = nil }, errs.ErrMissingDependency},
		{"empty dir", func(c *Config) { c.Dir = "" }, errs.ErrInvalidKey},
		{"empty server id", func(c *Config) { c.ServerID = "" }, errs.ErrInvalidKey},
		{"zero time", func(c *Config) { c.Now = time.Time{} }, errs.ErrInvalidKey},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t.TempDir(), nil, testNow)
			tc.mutate(&cfg)
			if _, err := LoadOrCreateWith(cfg); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestLoadOrCreateBindingUsesSystemSources(t *testing.T) {
	dir := t.TempDir()
	material, err := LoadOrCreate(dir, testServerID, []string{"bound.example.test"}, testNow)
	if err != nil {
		t.Fatalf("LoadOrCreate: %v", err)
	}
	if !slices.Contains(material.Certificate.Leaf.DNSNames, "bound.example.test") {
		t.Fatalf("DNS SANs = %v", material.Certificate.Leaf.DNSNames)
	}
}

func TestMaterialServesTLS13PinnedHandshake(t *testing.T) {
	material := mustLoad(t, testConfig(t.TempDir(), nil, testNow))
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	server := tls.Server(serverConn, &tls.Config{Certificates: []tls.Certificate{material.Certificate}, MinVersion: tls.VersionTLS13})
	done := make(chan error, 1)
	go func() { done <- server.Handshake() }()

	var pinned [32]byte
	client := tls.Client(clientConn, &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			pinned = sha256.Sum256(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
			return nil
		},
	})
	if err := client.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	if pinned != material.SPKISHA256 {
		t.Fatal("pin observed by the client differs from Material.SPKISHA256")
	}
}
