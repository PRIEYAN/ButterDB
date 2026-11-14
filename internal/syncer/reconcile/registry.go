package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	historyPageSize   = 100
	firstMarkerWindow = historyPageSize + 1
	writerRekey       = "sys.rekey"
	bucketTitlePrefix = "butterDB bucket "
)

var ErrConfirmationRequired = errors.New("reconcile: registry adoption needs an explicit channel id")
var ErrNotACandidate = errors.New("reconcile: channel is not a registry candidate")
var ErrForeignRegistry = errors.New("reconcile: registry belongs to another key or user")

type RegistryReader interface {
	ListOwnedChannels(ctx context.Context) ([]tgclient.Channel, error)
	GetHistory(ctx context.Context, peer tgclient.InputPeer, offsetID int64, limit int) ([]tgclient.HistoryMessage, error)
}

type Identity struct {
	KeyFingerprint string
	UserID         int64
}

type ForeignReason string

const (
	NotForeign     ForeignReason = ""
	ForeignKey     ForeignReason = "key"
	ForeignAccount ForeignReason = "user"
)

type Candidate struct {
	Peer             tgclient.InputPeer
	Title            string
	CreatedAt        int64
	DeploymentID     string
	FormatVersion    int
	MinReaderVersion int
	KeyFingerprint   string
	UserID           int64
	MarkerMsgID      int64
	LiveBuckets      int
	Foreign          ForeignReason
}

func (c Candidate) Eligible() bool {
	return c.Foreign == NotForeign
}

type Adoption struct {
	RegistryChannelID    int64
	RegistryAccessHash   int64
	RegistryMarkerMsgID  int64
	PrimaryUserID        int64
	ServerKeyFingerprint string
	DeploymentID         string
	FormatVersion        int
	MinReaderVersion     int
}

type DiscoveryDependencies struct {
	Telegram RegistryReader
	Identity Identity
	Logger   *slog.Logger
}

type Discoverer struct {
	tg       RegistryReader
	identity Identity
	log      *slog.Logger
}

func NewDiscoverer(deps DiscoveryDependencies) (*Discoverer, error) {
	if deps.Telegram == nil {
		return nil, fmt.Errorf("reconcile: %w: telegram", errs.ErrMissingDependency)
	}
	if deps.Identity.KeyFingerprint == "" {
		return nil, fmt.Errorf("reconcile: %w: key fingerprint", errs.ErrMissingDependency)
	}
	if deps.Identity.UserID <= 0 {
		return nil, fmt.Errorf("reconcile: %w: authorized user", errs.ErrMissingDependency)
	}
	if deps.Logger == nil {
		return nil, fmt.Errorf("reconcile: %w: logger", errs.ErrMissingDependency)
	}
	return &Discoverer{tg: deps.Telegram, identity: deps.Identity, log: deps.Logger}, nil
}

func Eligible(candidates []Candidate) []Candidate {
	var out []Candidate
	for _, c := range candidates {
		if c.Eligible() {
			out = append(out, c)
		}
	}
	return out
}

func (d *Discoverer) Discover(ctx context.Context) ([]Candidate, error) {
	channels, err := d.tg.ListOwnedChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconcile: list owned channels: %w", err)
	}
	var out []Candidate
	for _, ch := range channels {
		cand, ok, err := d.inspect(ctx, ch)
		if err != nil {
			if errors.Is(err, tgclient.ErrChannelUnavailable) {
				d.log.Warn("reconcile: owned channel is unavailable, skipping", "channel_id", ch.Peer.ChannelID)
				continue
			}
			return nil, err
		}
		if ok {
			out = append(out, cand)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Peer.ChannelID < out[j].Peer.ChannelID })
	return out, nil
}

func (d *Discoverer) Adopt(candidates []Candidate, confirmedChannelID int64) (Adoption, error) {
	if confirmedChannelID <= 0 {
		return Adoption{}, ErrConfirmationRequired
	}
	for _, c := range candidates {
		if c.Peer.ChannelID != confirmedChannelID {
			continue
		}
		if d.classify(c.KeyFingerprint, c.UserID) != NotForeign {
			return Adoption{}, fmt.Errorf("%w: channel %d", ErrForeignRegistry, confirmedChannelID)
		}
		return Adoption{
			RegistryChannelID:    c.Peer.ChannelID,
			RegistryAccessHash:   c.Peer.AccessHash,
			RegistryMarkerMsgID:  c.MarkerMsgID,
			PrimaryUserID:        c.UserID,
			ServerKeyFingerprint: c.KeyFingerprint,
			DeploymentID:         c.DeploymentID,
			FormatVersion:        c.FormatVersion,
			MinReaderVersion:     c.MinReaderVersion,
		}, nil
	}
	return Adoption{}, fmt.Errorf("%w: channel %d", ErrNotACandidate, confirmedChannelID)
}

func (d *Discoverer) classify(fingerprint string, userID int64) ForeignReason {
	if fingerprint != d.identity.KeyFingerprint {
		return ForeignKey
	}
	if userID != d.identity.UserID {
		return ForeignAccount
	}
	return NotForeign
}

type markerMessage struct {
	msgID int64
	date  int64
	op    wire.Op
}

func (d *Discoverer) inspect(ctx context.Context, ch tgclient.Channel) (Candidate, bool, error) {
	window, err := d.tg.GetHistory(ctx, ch.Peer, firstMarkerWindow, historyPageSize)
	if err != nil {
		return Candidate{}, false, fmt.Errorf("reconcile: read first messages of channel %d: %w", ch.Peer.ChannelID, err)
	}
	if len(markers(window)) == 0 {
		return Candidate{}, false, nil
	}
	history, err := d.readAll(ctx, ch.Peer)
	if err != nil {
		return Candidate{}, false, err
	}
	chain := markers(history)
	if len(chain) == 0 {
		return Candidate{}, false, nil
	}
	first := chain[0]
	current := first
	for _, next := range chain[1:] {
		if acceptRevision(current.op, next.op) {
			current = next
		}
	}
	cand := Candidate{
		Peer:             ch.Peer,
		Title:            ch.Title,
		CreatedAt:        first.date,
		DeploymentID:     current.op.DeploymentID,
		FormatVersion:    current.op.FormatVersion,
		MinReaderVersion: current.op.MinReaderVersion,
		KeyFingerprint:   current.op.KEKFingerprint,
		UserID:           current.op.PrimaryUserID,
		MarkerMsgID:      current.msgID,
		LiveBuckets:      liveBuckets(history),
	}
	cand.Foreign = d.classify(cand.KeyFingerprint, cand.UserID)
	return cand, true, nil
}

func (d *Discoverer) readAll(ctx context.Context, peer tgclient.InputPeer) ([]tgclient.HistoryMessage, error) {
	var all []tgclient.HistoryMessage
	offset := int64(0)
	for {
		page, err := d.tg.GetHistory(ctx, peer, offset, historyPageSize)
		if err != nil {
			return nil, fmt.Errorf("reconcile: read history of channel %d: %w", peer.ChannelID, err)
		}
		if len(page) == 0 {
			return all, nil
		}
		lowest := page[0].MsgID
		for _, m := range page {
			if m.MsgID < lowest {
				lowest = m.MsgID
			}
		}
		if offset > 0 && lowest >= offset {
			return nil, fmt.Errorf("reconcile: channel %d: %w", peer.ChannelID, syncer.ErrHistoryNoProgress)
		}
		all = append(all, page...)
		offset = lowest
	}
}

func textOp(m tgclient.HistoryMessage) (wire.Op, bool) {
	if m.Placeholder || m.HasMedia || m.MsgID <= 0 {
		return wire.Op{}, false
	}
	op, err := wire.Parse(m.Text)
	if err != nil {
		return wire.Op{}, false
	}
	return op, true
}

func markers(msgs []tgclient.HistoryMessage) []markerMessage {
	var out []markerMessage
	for _, m := range msgs {
		op, ok := textOp(m)
		if !ok || op.Type != wire.OpRegistry {
			continue
		}
		out = append(out, markerMessage{msgID: m.MsgID, date: m.Date, op: op})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].msgID < out[j].msgID })
	return out
}

func acceptRevision(current, next wire.Op) bool {
	if next.DeploymentID != current.DeploymentID || next.PrimaryUserID != current.PrimaryUserID {
		return false
	}
	if next.FormatVersion < current.FormatVersion || next.MinReaderVersion < current.MinReaderVersion {
		return false
	}
	return next.KEKFingerprint == current.KEKFingerprint || next.Writer == writerRekey
}

func liveBuckets(msgs []tgclient.HistoryMessage) int {
	type event struct {
		msgID   int64
		created bool
		channel int64
	}
	var events []event
	for _, m := range msgs {
		op, ok := textOp(m)
		if !ok {
			continue
		}
		switch op.Type {
		case wire.OpBucketCreate:
			events = append(events, event{msgID: m.MsgID, created: true, channel: op.BucketChannelID})
		case wire.OpBucketDelete:
			events = append(events, event{msgID: m.MsgID, channel: op.BucketChannelID})
		}
	}
	sort.Slice(events, func(i, j int) bool { return events[i].msgID < events[j].msgID })
	live := make(map[int64]struct{})
	for _, e := range events {
		if e.created {
			live[e.channel] = struct{}{}
			continue
		}
		delete(live, e.channel)
	}
	return len(live)
}

type BucketReferences interface {
	ReferencedBucketChannels(ctx context.Context) ([]int64, error)
}

type OrphanChannel struct {
	Peer          tgclient.InputPeer
	Title         string
	BucketName    string
	AboutVerified bool
}

type OrphanDependencies struct {
	Telegram interface {
		ListOwnedChannels(ctx context.Context) ([]tgclient.Channel, error)
	}
	Buckets BucketReferences
	Logger  *slog.Logger
}

type OrphanFinder struct {
	tg interface {
		ListOwnedChannels(ctx context.Context) ([]tgclient.Channel, error)
	}
	buckets BucketReferences
	log     *slog.Logger
}

func NewOrphanFinder(deps OrphanDependencies) (*OrphanFinder, error) {
	if deps.Telegram == nil {
		return nil, fmt.Errorf("reconcile: %w: telegram", errs.ErrMissingDependency)
	}
	if deps.Buckets == nil {
		return nil, fmt.Errorf("reconcile: %w: buckets", errs.ErrMissingDependency)
	}
	if deps.Logger == nil {
		return nil, fmt.Errorf("reconcile: %w: logger", errs.ErrMissingDependency)
	}
	return &OrphanFinder{tg: deps.Telegram, buckets: deps.Buckets, log: deps.Logger}, nil
}

func (f *OrphanFinder) List(ctx context.Context, deploymentID string) ([]OrphanChannel, error) {
	if deploymentID == "" {
		return nil, fmt.Errorf("reconcile: %w: deployment id", errs.ErrMissingDependency)
	}
	referenced, err := f.buckets.ReferencedBucketChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconcile: list referenced bucket channels: %w", err)
	}
	known := make(map[int64]struct{}, len(referenced))
	for _, id := range referenced {
		known[id] = struct{}{}
	}
	channels, err := f.tg.ListOwnedChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconcile: list owned channels: %w", err)
	}
	var out []OrphanChannel
	for _, ch := range channels {
		if _, ok := known[ch.Peer.ChannelID]; ok {
			continue
		}
		name, ok := strings.CutPrefix(ch.Title, bucketTitlePrefix)
		if !ok || name == "" {
			continue
		}
		verified := ch.About != ""
		if verified && !namesDeployment(ch.About, deploymentID) {
			continue
		}
		out = append(out, OrphanChannel{Peer: ch.Peer, Title: ch.Title, BucketName: name, AboutVerified: verified})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Peer.ChannelID < out[j].Peer.ChannelID })
	f.log.Info("reconcile: listed orphan bucket channels", "count", len(out))
	return out, nil
}

func namesDeployment(about, deploymentID string) bool {
	return strings.HasPrefix(about, bucketTitlePrefix) && strings.Contains(about, " in deployment "+deploymentID+".")
}
