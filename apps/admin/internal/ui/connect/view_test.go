package connect

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/connect/vm"
)

const (
	serverID  = "k3v9m2q8t4w7x1z5a6b2c3d4e5"
	baseURL   = "https://203.0.113.10:9443"
	pin       = "SHA256:4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e4f1c9a0e"
	otherPin  = "SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	setupTok  = "bdst1_abcdefghijklmnopqrstuvwxyz234567"
	deviceTok = "bdt1_a1b2c3d4e5f6g7h2_secretsecretsecretsecretsecretsecretsecretsecret"
)

var fixedNow = time.Date(2026, 11, 16, 9, 30, 0, 0, time.UTC)

type memTokens struct {
	mu     sync.Mutex
	kind   model.TokenStorage
	tokens map[string]string
	setErr error
}

func newMemTokens(kind model.TokenStorage) *memTokens {
	return &memTokens{kind: kind, tokens: map[string]string{}}
}

func (m *memTokens) Get(id string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.tokens[id]; ok {
		return t, nil
	}
	return "", model.ErrTokenNotFound
}

func (m *memTokens) Set(id, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.setErr != nil {
		return m.setErr
	}
	m.tokens[id] = token
	return nil
}

func (m *memTokens) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokens, id)
	return nil
}

func (m *memTokens) Kind() model.TokenStorage { return m.kind }

type memProfiles struct {
	mu       sync.Mutex
	profiles []model.Profile
	last     string
}

func (m *memProfiles) List() ([]model.Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.Profile(nil), m.profiles...), nil
}

func (m *memProfiles) Get(id string) (model.Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.profiles {
		if p.ServerID == id {
			return p, nil
		}
	}
	return model.Profile{}, model.ErrProfileNotFound
}

func (m *memProfiles) Put(p model.Profile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, existing := range m.profiles {
		if existing.ServerID == p.ServerID {
			m.profiles[i] = p
			return nil
		}
	}
	m.profiles = append(m.profiles, p)
	return nil
}

func (m *memProfiles) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, p := range m.profiles {
		if p.ServerID == id {
			m.profiles = append(m.profiles[:i], m.profiles[i+1:]...)
			return nil
		}
	}
	return model.ErrProfileNotFound
}

func (m *memProfiles) LastUsed() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last, nil
}

func (m *memProfiles) SetLastUsed(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.last = id
	return nil
}

type stubClient struct {
	info     adminv1.ServerInfo
	infoErr  error
	claimErr error
}

func (c *stubClient) ServerInfo(context.Context) (adminv1.ServerInfo, error) {
	return c.info, c.infoErr
}

func (c *stubClient) Claim(context.Context, adminv1.ClaimRequest) (adminv1.DeviceCredentials, error) {
	return adminv1.DeviceCredentials{
		Device:      adminv1.Device{DeviceID: "a1b2c3d4e5f6g7h2", Name: "Office laptop", TokenIssuedAt: fixedNow},
		DeviceToken: deviceTok,
		ServerID:    serverID,
	}, c.claimErr
}

func (c *stubClient) RequestPairing(context.Context, adminv1.PairingRequest) (adminv1.PairingRequested, error) {
	return adminv1.PairingRequested{Delivery: "saved_messages", ExpiresAt: fixedNow.Add(10 * time.Minute)}, nil
}

func (c *stubClient) CompletePairing(context.Context, adminv1.PairingCompleteRequest) (adminv1.DeviceCredentials, error) {
	return c.Claim(context.Background(), adminv1.ClaimRequest{})
}

func (c *stubClient) RotateDeviceToken(context.Context) (adminv1.DeviceCredentials, error) {
	return adminv1.DeviceCredentials{}, errors.New("not used")
}

func (c *stubClient) SetToken(string) {}

type stubDialer struct {
	client *stubClient
	probe  adminv1.CertificateInfo
}

func (d *stubDialer) Probe(context.Context, string) (adminv1.CertificateInfo, error) {
	return d.probe, nil
}

func (d *stubDialer) Dial(adminv1.ClientConfig) (vm.ConnectClient, error) { return d.client, nil }

type recordingNav struct {
	mu        sync.Mutex
	current   model.Route
	navigated []model.Route
}

func (n *recordingNav) Navigate(r model.Route, _ model.Params) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.navigated = append(n.navigated, r)
	n.current = r
}

func (n *recordingNav) Current() model.Route {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.current
}

func (n *recordingNav) SetEnabled(model.Route, bool, string) {}

func (n *recordingNav) routes() []model.Route {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]model.Route(nil), n.navigated...)
}

type fakeClipboard struct{ text string }

func (c *fakeClipboard) SetText(text string) { c.text = text }
func (c *fakeClipboard) Text() string        { return c.text }

type harness struct {
	t         *testing.T
	vm        *vm.ViewModel
	view      *view
	nav       *recordingNav
	client    *stubClient
	dialer    *stubDialer
	keychain  *memTokens
	profiles  *memProfiles
	clip      *fakeClipboard
	connected chan model.Profile
	queue     uiQueue
}

type uiQueue struct {
	mu    sync.Mutex
	tasks []func()
}

func (u *uiQueue) push(task func()) {
	u.mu.Lock()
	u.tasks = append(u.tasks, task)
	u.mu.Unlock()
}

func (u *uiQueue) pop() func() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.tasks) == 0 {
		return nil
	}
	task := u.tasks[0]
	u.tasks = u.tasks[1:]
	return task
}

func (h *harness) pump() {
	for task := h.queue.pop(); task != nil; task = h.queue.pop() {
		task()
	}
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	test.NewTempApp(t)
	h := &harness{
		t:         t,
		nav:       &recordingNav{current: model.RouteConnect},
		keychain:  newMemTokens(model.TokenKeychain),
		profiles:  &memProfiles{},
		clip:      &fakeClipboard{},
		connected: make(chan model.Profile, 4),
	}
	h.client = &stubClient{info: adminv1.ServerInfo{
		ServerID:   serverID,
		APIVersion: "v1",
		State:      "unclaimed",
		TLS:        adminv1.TLSInfo{Mode: "self-signed", SPKISHA256: pin},
	}}
	dialer := &stubDialer{client: h.client, probe: adminv1.CertificateInfo{
		SPKISHA256:         pin,
		DisplayFingerprint: "4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e 4f1c 9a0e",
		Subject:            "CN=butterdbd",
		IPAddresses:        []string{"203.0.113.10"},
		NotAfter:           fixedNow.Add(24 * time.Hour),
	}}
	h.dialer = dialer
	v, err := vm.New(vm.Dependencies{
		Dialer:            dialer,
		Tokens:            vm.TokenStores{Keychain: h.keychain, File: newMemTokens(model.TokenFile), Memory: newMemTokens(model.TokenMemory)},
		Profiles:          h.profiles,
		Now:               func() time.Time { return fixedNow },
		OnConnected:       func(p model.Profile) { h.connected <- p },
		DefaultDeviceName: "Office laptop",
		Platform:          "linux/amd64",
		ClientVersion:     "0.1.0",
	})
	if err != nil {
		t.Fatalf("vm.New: %v", err)
	}
	h.vm = v
	h.view = newView(v, h.nav, h.clip, func() time.Time { return fixedNow }, h.queue.push)
	test.NewTempWindow(t, h.view.root)
	return h
}

func (h *harness) waitStep(step vm.Step) {
	h.t.Helper()
	h.waitFor(func() bool { return h.vm.State().Get().Step == step && !h.vm.State().Get().Loading }, "step "+string(step))
}

func (h *harness) waitFor(cond func() bool, what string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.pump()
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}

func (h *harness) panelVisible(step vm.Step) bool {
	return h.view.panels[step].object().Visible()
}

func (h *harness) seedProfile(label string) model.Profile {
	h.t.Helper()
	p := model.Profile{
		ServerID:     serverID,
		Label:        label,
		BaseURL:      baseURL,
		Trust:        adminv1.Trust{Mode: adminv1.TrustPinned, SPKISHA256: pin},
		DeviceID:     "a1b2c3d4e5f6g7h2",
		TokenStorage: model.TokenKeychain,
		LastUsedAt:   fixedNow.Add(-2 * time.Hour),
	}
	if err := h.profiles.Put(p); err != nil {
		h.t.Fatal(err)
	}
	if err := h.keychain.Set(serverID, deviceTok); err != nil {
		h.t.Fatal(err)
	}
	return p
}

func (h *harness) toClaim() {
	h.t.Helper()
	ctx := context.Background()
	h.vm.Continue(ctx, "203.0.113.10", "")
	h.vm.ConfirmCertificate(ctx, false)
	h.waitStep(vm.StepClaim)
}

func tapButton(b *actionButton) { test.Tap(&b.Button) }

func TestViewListsProfilesWithActions(t *testing.T) {
	h := newHarness(t)
	h.seedProfile("home server")
	h.vm.Refresh(context.Background())
	h.pump()
	panel := h.view.panels[vm.StepProfiles].(*profilesPanel)
	if !h.panelVisible(vm.StepProfiles) {
		t.Fatalf("profiles panel hidden")
	}
	if len(panel.list.Objects) != 1 {
		t.Fatalf("profile rows = %d, want 1", len(panel.list.Objects))
	}
	if panel.empty.Visible() {
		t.Fatalf("empty hint shown with a profile present")
	}
	if !labelTextPresent(panel.list, "home server") || !labelTextPresent(panel.list, trustPinnedText) {
		t.Fatalf("row lacks label or trust mode text")
	}
	for _, name := range []string{"Connect", "Rename", "Forget"} {
		if !buttonPresent(panel.list, name) {
			t.Fatalf("row lacks the %s button", name)
		}
	}
}

func TestViewConnectButtonConnectsProfile(t *testing.T) {
	h := newHarness(t)
	h.seedProfile("home server")
	h.vm.Refresh(context.Background())
	h.pump()
	panel := h.view.panels[vm.StepProfiles].(*profilesPanel)
	connect := findButtonByText(panel.list, "Connect")
	test.Tap(connect)
	select {
	case p := <-h.connected:
		if p.ServerID != serverID {
			t.Fatalf("connected %q", p.ServerID)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Connect button did not connect")
	}
}

func TestViewAddServerShowsCertificateStep(t *testing.T) {
	h := newHarness(t)
	panel := h.view.panels[vm.StepProfiles].(*profilesPanel)
	test.Type(panel.urlEntry, "203.0.113.10")
	tapButton(panel.continues)
	h.waitStep(vm.StepConfirmCertificate)
	cert := h.view.panels[vm.StepConfirmCertificate].(*certificatePanel)
	if !h.panelVisible(vm.StepConfirmCertificate) || h.panelVisible(vm.StepProfiles) {
		t.Fatalf("certificate panel not the only visible panel")
	}
	want := "4f1c 9a0e 4f1c 9a0e\n4f1c 9a0e 4f1c 9a0e\n4f1c 9a0e 4f1c 9a0e\n4f1c 9a0e 4f1c 9a0e"
	if cert.fingerprint.Text != want || !cert.fingerprint.Selectable {
		t.Fatalf("fingerprint label = %q selectable %v", cert.fingerprint.Text, cert.fingerprint.Selectable)
	}
	if cert.confirm.Text != matchLabel || cert.cancel.Importance != 1 && cert.cancel.Importance == cert.confirm.Importance {
		t.Fatalf("confirm %q: cancel must be the emphasised default", cert.confirm.Text)
	}
	if !cert.instructions.Visible() {
		t.Fatalf("log instructions hidden for a self-signed certificate")
	}
	tapButton(cert.cancel)
	h.waitStep(vm.StepProfiles)
}

func TestViewSystemTrustedCertificateOffersTrustOrPin(t *testing.T) {
	h := newHarness(t)
	h.dialer.probe.VerifiesWithSystemRoots = true
	h.vm.Continue(context.Background(), "butterdb.example", "")
	h.pump()
	cert := h.view.panels[vm.StepConfirmCertificate].(*certificatePanel)
	if cert.confirm.Text != trustSystemLabel || cert.instructions.Visible() || !cert.pinInstead.Visible() {
		t.Fatalf("confirm %q instructions visible %v pin toggle visible %v", cert.confirm.Text, cert.instructions.Visible(), cert.pinInstead.Visible())
	}
	test.Tap(cert.pinInstead)
	if cert.confirm.Text != pinKeyLabel {
		t.Fatalf("confirm after toggling pin = %q", cert.confirm.Text)
	}
}

func TestViewBadURLShowsInlineProblem(t *testing.T) {
	h := newHarness(t)
	panel := h.view.panels[vm.StepProfiles].(*profilesPanel)
	test.Type(panel.urlEntry, "http://butterdb.example")
	tapButton(panel.continues)
	h.waitFor(func() bool { return h.vm.State().Get().Problem != nil }, "url problem")
	if len(h.view.problem.Objects) != 1 {
		t.Fatalf("problem strip empty")
	}
}

func TestViewClaimClearsSetupTokenEntry(t *testing.T) {
	h := newHarness(t)
	h.toClaim()
	claim := h.view.panels[vm.StepClaim].(*claimPanel)
	if claim.device.Text != "Office laptop" {
		t.Fatalf("device name default = %q", claim.device.Text)
	}
	test.Type(claim.token, setupTok)
	if got := h.vm.State().Get().SetupToken; got != setupTok {
		t.Fatalf("view model token = %q", got)
	}
	tapButton(claim.claim)
	select {
	case p := <-h.connected:
		if p.TokenStorage != model.TokenKeychain {
			t.Fatalf("token storage %q", p.TokenStorage)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("claim did not connect")
	}
	h.waitFor(func() bool { return claim.token.Text == "" }, "setup token entry cleared")
	if h.vm.State().Get().SetupToken != "" {
		t.Fatalf("view model still holds the setup token")
	}
	if got, _ := h.keychain.Get(serverID); got != deviceTok {
		t.Fatalf("keychain token = %q", got)
	}
	if !h.panelVisible(vm.StepProfiles) {
		t.Fatalf("profiles panel not shown after connecting")
	}
}

func TestViewClaimTokenIsPasswordEntry(t *testing.T) {
	h := newHarness(t)
	claim := h.view.panels[vm.StepClaim].(*claimPanel)
	if !claim.token.Password {
		t.Fatalf("setup token entry is not masked")
	}
}

func TestViewPairingCodeFormatsAsTyped(t *testing.T) {
	h := newHarness(t)
	h.client.info.State = "claimed"
	h.client.info.PairingMethods = []string{"host", "saved_messages"}
	ctx := context.Background()
	h.vm.Continue(ctx, "203.0.113.10", "")
	h.vm.ConfirmCertificate(ctx, false)
	h.waitStep(vm.StepPair)
	pair := h.view.panels[vm.StepPair].(*pairPanel)
	if !pair.pair.Disabled() {
		t.Fatalf("Pair enabled with an empty code")
	}
	test.Type(pair.code, "abcdefg")
	if pair.code.Text != "ABCDE-FG" {
		t.Fatalf("code entry = %q, want ABCDE-FG", pair.code.Text)
	}
	h.pump()
	test.Type(pair.code, "hjk")
	if pair.code.Text != "ABCDE-FGHJK" {
		t.Fatalf("code entry = %q, want ABCDE-FGHJK", pair.code.Text)
	}
	h.pump()
	h.waitFor(func() bool { return !pair.pair.Disabled() }, "Pair enabled at 10 characters")
	if pair.request.Disabled() {
		t.Fatalf("Saved Messages button disabled although the server offers it")
	}
	if pair.offNotice.Visible() {
		t.Fatalf("unlinked notice shown although Saved Messages is available")
	}
}

func TestViewPairingSavedMessagesDisabledWhenUnlinked(t *testing.T) {
	h := newHarness(t)
	h.client.info.State = "claimed"
	h.client.info.PairingMethods = []string{"host"}
	ctx := context.Background()
	h.vm.Continue(ctx, "203.0.113.10", "")
	h.vm.ConfirmCertificate(ctx, false)
	h.waitStep(vm.StepPair)
	pair := h.view.panels[vm.StepPair].(*pairPanel)
	if !pair.request.Disabled() || !pair.offNotice.Visible() || pair.offNotice.Text != savedMessagesOff {
		t.Fatalf("request disabled=%v notice visible=%v text=%q", pair.request.Disabled(), pair.offNotice.Visible(), pair.offNotice.Text)
	}
}

func TestViewPairingRequestShowsExpiry(t *testing.T) {
	h := newHarness(t)
	h.client.info.State = "claimed"
	h.client.info.PairingMethods = []string{"saved_messages"}
	ctx := context.Background()
	h.vm.Continue(ctx, "203.0.113.10", "")
	h.vm.ConfirmCertificate(ctx, false)
	h.waitStep(vm.StepPair)
	pair := h.view.panels[vm.StepPair].(*pairPanel)
	tapButton(pair.request)
	h.waitFor(func() bool { return pair.sent.Visible() && pair.sent.Text != "" }, "expiry text")
	if pair.sent.Text != "Code sent. It expires in 10:00." {
		t.Fatalf("expiry text = %q", pair.sent.Text)
	}
	pair.stopCountdown()
}

func TestViewPinMismatchBlocksAndRoutesToConnect(t *testing.T) {
	h := newHarness(t)
	h.seedProfile("home server")
	h.nav.current = model.RouteDashboard
	handled := h.vm.HandleSessionError(context.Background(), serverID, &adminv1.PinMismatchError{Expected: pin, Got: otherPin})
	if !handled {
		t.Fatalf("pin mismatch not handled")
	}
	h.waitFor(h.panelPinMismatchVisible, "mismatch panel")
	mismatch := h.view.panels[vm.StepPinMismatch].(*mismatchPanel)
	if !labelTextPresent(mismatch.body, "Expected") && len(mismatch.body.Objects) != 3 {
		t.Fatalf("mismatch body = %d objects", len(mismatch.body.Objects))
	}
	if mismatch.disconnect.Importance == mismatch.trustNew.Importance {
		t.Fatalf("Disconnect must be the emphasised default")
	}
	h.pump()
	h.waitFor(func() bool { return len(h.nav.routes()) > 0 }, "navigation to connect")
	if got := h.nav.routes()[0]; got != model.RouteConnect {
		t.Fatalf("navigated to %q", got)
	}
	tapButton(mismatch.disconnect)
	h.waitStep(vm.StepProfiles)
}

func (h *harness) panelPinMismatchVisible() bool {
	return h.view.panels[vm.StepPinMismatch].object().Visible() && h.vm.State().Get().Mismatch != nil
}

func TestViewStorageChoiceFallsBack(t *testing.T) {
	h := newHarness(t)
	h.keychain.setErr = model.ErrKeychainUnavailable
	h.toClaim()
	claim := h.view.panels[vm.StepClaim].(*claimPanel)
	test.Type(claim.token, setupTok)
	tapButton(claim.claim)
	h.waitStep(vm.StepStorageChoice)
	storage := h.view.panels[vm.StepStorageChoice].(*storagePanel)
	if storage.choice.Selected != sessionChoice {
		t.Fatalf("default storage choice = %q, want session only", storage.choice.Selected)
	}
	storage.choice.SetSelected(fileChoice)
	if storage.warning.Text != fileWarning {
		t.Fatalf("file warning = %q", storage.warning.Text)
	}
	storage.choice.SetSelected(sessionChoice)
	tapButton(storage.proceed)
	select {
	case p := <-h.connected:
		if p.TokenStorage != model.TokenMemory {
			t.Fatalf("token storage %q, want memory", p.TokenStorage)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("storage choice did not connect")
	}
}

func TestScreenContract(t *testing.T) {
	h := newHarness(t)
	s := shell.Screen(&screen{view: h.view})
	if s.Route() != model.RouteConnect || s.Title() != "Connect" || s.Shortcut() != fyne.KeyName("") {
		t.Fatalf("route %q title %q shortcut %q", s.Route(), s.Title(), s.Shortcut())
	}
	if s.Icon() == nil || s.Icon().Name() != theme.ComputerIcon().Name() || s.Content() == nil {
		t.Fatalf("icon or content missing")
	}
	h.seedProfile("home server")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Show(ctx, nil)
	h.waitFor(func() bool { return len(h.vm.State().Get().Profiles) == 1 }, "profiles refreshed on Show")
	s.Hide()
}

func TestConstructorsReturnContent(t *testing.T) {
	h := newHarness(t)
	if NewView(h.vm, h.nav) == nil || NewScreen(h.vm, h.nav) == nil {
		t.Fatalf("constructor returned nil")
	}
}
