package dashboard

import (
	"context"
	"strconv"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/shell"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/ui/dashboard/vm"
)

const (
	endpointsHint = "Use these with any AWS SDK: endpoint, region, path-style addressing."
	pageKicker    = "admin / dashboard"
	pairNameEms   = 12
)

type view struct {
	vm       *vm.ViewModel
	nav      shell.Navigator
	notifier shell.Notifier
	clip     model.Clipboard

	root     fyne.CanvasObject
	problem  *fyne.Container
	updated  *widget.Label
	refresh  *widget.Button
	server   *fyne.Container
	telegram *fyne.Container
	storage  *fyne.Container
	cache    *fyne.Container
	comps    *fyne.Container
	ends     *fyne.Container
	replica  *shell.WindowCard
	headline *shell.WindowCard
	replBody *fyne.Container

	mu          sync.Mutex
	ctx         context.Context
	bannerShown bool
	dialogShown string
	routed      string
}

func newView(v *vm.ViewModel, nav shell.Navigator, n shell.Notifier, clip model.Clipboard) *view {
	w := &view{vm: v, nav: nav, notifier: n, clip: clip, ctx: context.Background()}
	w.build()
	w.render(v.State().Get())
	v.State().Subscribe(func(s vm.State) {
		fyne.Do(func() { w.render(s) })
	})
	return w
}

func (w *view) build() {
	w.problem = container.NewVBox()
	w.updated = shell.NewKicker("")
	w.refresh = widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), w.onRefresh)
	w.server, w.telegram, w.storage = container.NewVBox(), container.NewVBox(), container.NewVBox()
	w.cache, w.comps, w.ends = container.NewVBox(), container.NewVBox(), container.NewVBox()
	w.replBody = container.NewVBox()
	w.replica = shell.NewWindowCard("dashboard/replica", "Replica", w.replBody)
	w.headline = shell.NewHighlightCard("dashboard/server", "Server", w.server)
	cards := shell.NewCardGrid(2,
		shell.NewWindowCard("dashboard/telegram", "Telegram", w.telegram),
		shell.NewWindowCard("dashboard/storage", "Storage", w.storage),
		shell.NewWindowCard("dashboard/cache", "Cache and spool", w.cache),
		shell.NewWindowCard("dashboard/components", "Components", w.comps),
		shell.NewWindowCard("dashboard/endpoints", "Endpoints", w.ends),
		w.replica,
	)
	trailing := container.NewHBox(container.NewCenter(w.updated), shell.Action(w.refresh))
	header := shell.NewPageHeader(pageKicker, shell.NewHeading("Dashboard"), trailing)
	body := container.NewVBox(w.headline, shell.NewSectionGap(), cards)
	w.root = container.NewBorder(container.NewVBox(header, w.problem), nil, nil, nil, shell.ScrollBody(body))
}

func (w *view) setContext(ctx context.Context) {
	w.mu.Lock()
	w.ctx = ctx
	w.mu.Unlock()
}

func (w *view) context() context.Context {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ctx
}

func (w *view) onRefresh() {
	ctx := w.context()
	go w.vm.Refresh(ctx)
}

func (w *view) render(s vm.State) {
	now := w.vm.Now()
	w.toggleRefresh(s.Loading)
	w.updated.SetText(updatedText(s, now))
	fill(w.server, serverRows(s))
	w.headline.Refresh()
	fill(w.telegram, telegramRows(s, now))
	fill(w.storage, storageRows(s))
	fill(w.cache, cacheRows(s))
	fill(w.comps, componentRows(s, now))
	fill(w.ends, w.endpointRows(s))
	w.renderReplica(s)
	w.renderProblem(s)
}

func (w *view) toggleRefresh(loading bool) {
	if loading {
		w.refresh.Disable()
		return
	}
	w.refresh.Enable()
}

func updatedText(s vm.State, now time.Time) string {
	switch {
	case s.Loading:
		return "Refreshing…"
	case s.RefreshedAt.IsZero():
		return ""
	}
	return "Updated " + shell.FormatAge(s.RefreshedAt, now)
}

func (w *view) renderReplica(s vm.State) {
	if !s.ReplicaVisible() {
		w.replica.Hide()
		return
	}
	fill(w.replBody, replicaRows(s))
	w.replica.Show()
}

func (w *view) endpointRows(s vm.State) []fyne.CanvasObject {
	if !s.HasStatus {
		return []fyne.CanvasObject{placeholderRow()}
	}
	ends := s.Status.Endpoints
	if ends.S3PublicURL == "" {
		return []fyne.CanvasObject{widget.NewLabel("S3 public URL is not configured on the server.")}
	}
	rows := []fyne.CanvasObject{shell.NewCopyField("S3 URL", ends.S3PublicURL, w.clip)}
	if ends.S3Region != "" {
		rows = append(rows, shell.NewCopyField("Region", ends.S3Region, w.clip))
	}
	return append(rows, newWrapped(endpointsHint))
}

func (w *view) renderProblem(s vm.State) {
	if s.Problem == nil {
		w.clearProblem()
		return
	}
	p := *s.Problem
	w.problem.Objects = []fyne.CanvasObject{problemStrip(p)}
	w.problem.Refresh()
	w.route(p)
}

func (w *view) clearProblem() {
	w.problem.Objects = nil
	w.problem.Refresh()
	w.mu.Lock()
	wasBanner := w.bannerShown
	w.bannerShown, w.dialogShown, w.routed = false, "", ""
	w.mu.Unlock()
	if wasBanner && w.notifier != nil {
		w.notifier.ClearBanner()
	}
}

func (w *view) route(p model.Problem) {
	key := string(p.Code) + "/" + p.RequestID + "/" + p.Title
	w.mu.Lock()
	defer w.mu.Unlock()
	switch p.Presentation {
	case model.PresentRoute:
		w.routeProblem(p, key)
	case model.PresentBanner:
		if w.notifier != nil {
			w.bannerShown = true
			w.notifier.Problem(p)
		}
	case model.PresentDialog:
		if w.notifier != nil && w.dialogShown != key {
			w.dialogShown = key
			w.notifier.Problem(p)
		}
	}
}

func (w *view) routeProblem(p model.Problem, key string) {
	if w.routed == key {
		return
	}
	w.routed = key
	if w.notifier != nil {
		w.notifier.Problem(p)
		return
	}
	if p.Route != "" && w.nav != nil {
		w.nav.Navigate(p.Route, nil)
	}
}

func fill(box *fyne.Container, rows []fyne.CanvasObject) {
	box.Objects = rows
	box.Refresh()
}

func problemStrip(p model.Problem) fyne.CanvasObject {
	rows := []fyne.CanvasObject{shell.NewFieldLabel(p.Title)}
	if p.Body != "" {
		rows = append(rows, newWrapped(p.Body))
	}
	return container.NewVBox(shell.NewCallout(shell.CalloutWarning, container.NewVBox(rows...)), shell.NewSectionGap())
}

func newWrapped(text string) *widget.Label {
	l := widget.NewLabel(text)
	l.Wrapping = fyne.TextWrapWord
	return l
}

func placeholderRow() fyne.CanvasObject {
	return widget.NewLabel("Waiting for data…")
}

func statusRow(word, text string) fyne.CanvasObject {
	return container.NewHBox(widget.NewIcon(statusIcon(word)), widget.NewLabel(text))
}

func nameCell(name string) fyne.CanvasObject {
	return container.NewStack(shell.FixedSpace(theme.TextSize()*pairNameEms, 0), shell.NewFieldLabel(name))
}

func pairRow(name, value string) fyne.CanvasObject {
	label := shell.NewMono(value)
	label.Selectable = true
	return container.NewBorder(nil, nil, nameCell(name), nil, label)
}

func textRow(name, value string) fyne.CanvasObject {
	label := widget.NewLabel(value)
	label.Selectable = true
	return container.NewBorder(nil, nil, nameCell(name), nil, label)
}

func meterRow(name string, fraction float64, text string) fyne.CanvasObject {
	return container.NewVBox(shell.NewFieldLabel(name), shell.NewMeter(fraction, text))
}

func serverRows(s vm.State) []fyne.CanvasObject {
	if !s.HasStatus {
		return []fyne.CanvasObject{placeholderRow()}
	}
	st := s.Status
	word := serverStatusWord(st.State)
	state := container.NewHBox(widget.NewIcon(shell.OnAccentIcon(statusGlyph(word))), widget.NewLabel("State: "+st.State))
	return []fyne.CanvasObject{container.NewGridWithColumns(3,
		state,
		pairRow("Version", versionText(st.ServerVersion, st.Commit)),
		pairRow("Uptime", uptimeText(st.UptimeSeconds)),
	)}
}

func telegramRows(s vm.State, now time.Time) []fyne.CanvasObject {
	if !s.HasStatus {
		return []fyne.CanvasObject{placeholderRow()}
	}
	t := s.Status.Telegram
	rows := []fyne.CanvasObject{accountRow(t.Account, t.Linked)}
	rows = append(rows, connectionRow(t))
	if s.FloodWaitActive(now) {
		rows = append(rows, statusRow(statusDegraded, floodWaitText(*t.FloodWaitUntil, now)))
	}
	return append(rows, pairRow("Send rate", strconv.Itoa(t.SendRatePerMinute)+" per minute"))
}

func accountRow(a *adminv1.TelegramAccount, linked bool) fyne.CanvasObject {
	if !linked || a == nil {
		return widget.NewLabel("No Telegram account linked.")
	}
	name := a.FirstName
	if a.Username != "" {
		name += " @" + a.Username
	}
	return textRow("Account", name)
}

func connectionRow(t adminv1.TelegramHealth) fyne.CanvasObject {
	if t.Connected {
		return statusRow(statusOK, "Connected")
	}
	return statusRow(statusDown, "Disconnected")
}

func storageRows(s vm.State) []fyne.CanvasObject {
	if !s.HasUsage {
		return []fyne.CanvasObject{placeholderRow()}
	}
	u := s.Usage
	return []fyne.CanvasObject{
		pairRow("Buckets", strconv.Itoa(u.Buckets)),
		pairRow("Objects", strconv.FormatInt(u.Objects, 10)),
		pairRow("Stored", shell.FormatBytes(u.StoredBytes)),
		pairRow("Plaintext", shell.FormatBytes(u.PlaintextBytes)),
		pairRow("Encrypted objects", strconv.FormatInt(u.EncryptedObjects, 10)),
	}
}

func cacheRows(s vm.State) []fyne.CanvasObject {
	if !s.HasUsage {
		return []fyne.CanvasObject{placeholderRow()}
	}
	u := s.Usage
	rows := blockCacheRows(s)
	return append(rows,
		meterRow("Spool", s.SpoolFraction(), usedOfLimitText(u.Spool.UsedBytes, u.Spool.LimitBytes)),
		pairRow("Jobs in flight", strconv.Itoa(u.Spool.JobsInFlight)),
	)
}

func blockCacheRows(s vm.State) []fyne.CanvasObject {
	if s.BlockCacheOff() {
		return []fyne.CanvasObject{container.NewBorder(nil, nil, nameCell("Block cache"), nil, container.NewHBox(shell.NewValueBadge("Off")))}
	}
	u := s.Usage
	return []fyne.CanvasObject{
		meterRow("Block cache", s.CacheFraction(), usedOfLimitText(u.BlockCache.UsedBytes, u.BlockCache.LimitBytes)),
		pairRow("Hit ratio (1 h)", percentText(u.BlockCache.HitRatio1h)),
	}
}

func componentRows(s vm.State, now time.Time) []fyne.CanvasObject {
	if !s.HasStatus {
		return []fyne.CanvasObject{placeholderRow()}
	}
	rows := make([]fyne.CanvasObject, 0, len(s.Status.Components))
	for _, c := range s.Status.Components {
		word := componentWord(c.Status)
		text := c.Name + ": " + word
		if c.Detail != "" {
			text += " — " + c.Detail
		}
		text += " (" + shell.FormatAge(c.Since, now) + ")"
		rows = append(rows, statusRow(word, text))
	}
	return rows
}

func replicaRows(s vm.State) []fyne.CanvasObject {
	r := s.Status.Replica
	return []fyne.CanvasObject{
		pairRow("State", r.State),
		pairRow("Pending ops", strconv.FormatInt(r.PendingOps, 10)),
	}
}
