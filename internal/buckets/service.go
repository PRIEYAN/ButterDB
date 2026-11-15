package buckets

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"

	"golang.org/x/sync/semaphore"

	"github.com/PRIEYAN/ButterDB/internal/domain/bucket"
	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/domain/limits"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/syncer"
	"github.com/PRIEYAN/ButterDB/internal/wire"
)

const (
	writerBuckets        = "sys.buckets"
	bucketChannelTitle   = "butterDB bucket "
	purposeBucketConfig  = "bcfg"
	purposeBucketDelete  = "bdel"
	purposeEncryptionKey = "encfg"
)

type Service struct {
	publisher    *opPublisher
	buckets      BucketReader
	channels     ChannelRecords
	scanner      EmptyChannelSyncer
	gate         ChannelGate
	creator      ChannelCreator
	keys         KeyMinter
	keyPresence  KeyPresence
	jobs         ActiveJobs
	writeGates   BucketWriteGate
	random       io.Reader
	clock        clock.Clock
	logger       *slog.Logger
	deploymentID string
	registryLock *semaphore.Weighted
}

func NewService(deps Dependencies) (*Service, error) {
	publisher, err := newOpPublisher(deps.Publish)
	if err != nil {
		return nil, err
	}
	switch {
	case deps.Buckets == nil:
		return nil, fmt.Errorf("buckets: %w: buckets", errs.ErrMissingDependency)
	case deps.Scanner == nil:
		return nil, fmt.Errorf("buckets: %w: scanner", errs.ErrMissingDependency)
	case deps.Gate == nil:
		return nil, fmt.Errorf("buckets: %w: channel gate", errs.ErrMissingDependency)
	case deps.Creator == nil:
		return nil, fmt.Errorf("buckets: %w: channel creator", errs.ErrMissingDependency)
	case deps.Keys == nil:
		return nil, fmt.Errorf("buckets: %w: keys", errs.ErrMissingDependency)
	case deps.KeyPresence == nil:
		return nil, fmt.Errorf("buckets: %w: key presence", errs.ErrMissingDependency)
	case deps.Jobs == nil:
		return nil, fmt.Errorf("buckets: %w: jobs", errs.ErrMissingDependency)
	case deps.WriteGates == nil:
		return nil, fmt.Errorf("buckets: %w: write gates", errs.ErrMissingDependency)
	case deps.Random == nil:
		return nil, fmt.Errorf("buckets: %w: random", errs.ErrMissingDependency)
	case deps.DeploymentID == "":
		return nil, fmt.Errorf("buckets: %w: deployment id", errs.ErrMissingDependency)
	}
	return &Service{
		publisher:    publisher,
		buckets:      deps.Buckets,
		channels:     deps.Publish.Channels,
		scanner:      deps.Scanner,
		gate:         deps.Gate,
		creator:      deps.Creator,
		keys:         deps.Keys,
		keyPresence:  deps.KeyPresence,
		jobs:         deps.Jobs,
		writeGates:   deps.WriteGates,
		random:       deps.Random,
		clock:        deps.Publish.Clock,
		logger:       deps.Publish.Logger,
		deploymentID: deps.DeploymentID,
		registryLock: semaphore.NewWeighted(1),
	}, nil
}

func (s *Service) Get(ctx context.Context, name bucket.BucketName) (bucket.Info, error) {
	if err := validateName(name); err != nil {
		return bucket.Info{}, err
	}
	info, found, err := s.buckets.Lookup(ctx, name)
	if err != nil {
		return bucket.Info{}, fmt.Errorf("buckets: get %q: %w", name, err)
	}
	if !found {
		return bucket.Info{}, fmt.Errorf("buckets: get %q: %w", name, errs.ErrNoSuchBucket)
	}
	return info, nil
}

func (s *Service) List(ctx context.Context) ([]bucket.Info, error) {
	infos, err := s.buckets.ListLive(ctx)
	if err != nil {
		return nil, fmt.Errorf("buckets: list: %w", err)
	}
	return infos, nil
}

func (s *Service) Create(ctx context.Context, spec bucket.Spec) (bucket.Info, error) {
	if err := validateSpec(spec); err != nil {
		return bucket.Info{}, err
	}
	release, err := s.lockRegistry(ctx)
	if err != nil {
		return bucket.Info{}, err
	}
	defer release()
	registry, err := s.authoritativeRegistry(ctx)
	if err != nil {
		return bucket.Info{}, err
	}
	if err := s.requireRoomForBucket(ctx, spec.Name); err != nil {
		return bucket.Info{}, err
	}
	channel, resumed, err := s.provisionalOrNewChannel(ctx, spec.Name)
	if err != nil {
		return bucket.Info{}, err
	}
	if err := s.markEmptyAuthoritative(ctx, channel); err != nil {
		return bucket.Info{}, err
	}
	_, err = s.publisher.publish(ctx, publication{
		channel: registry,
		op:      s.bucketCreateOp(spec, channel.ChannelID),
		seed:    derivedCreateJobID(s.deploymentID, channel.ChannelID),
		purpose: purposeBucketCreate,
		resumed: resumed,
	})
	if err != nil {
		return bucket.Info{}, fmt.Errorf("buckets: create %q: %w", spec.Name, err)
	}
	info, err := s.committedBucket(ctx, channel.ChannelID)
	if err != nil {
		return bucket.Info{}, err
	}
	if spec.DefaultEncryption == bucket.EncryptionTDE1 {
		s.publishInitialKey(ctx, registry, info.ChannelID)
	}
	return info, nil
}

func (s *Service) Update(ctx context.Context, name bucket.BucketName, patch bucket.Patch) (bucket.Info, error) {
	if err := validateName(name); err != nil {
		return bucket.Info{}, err
	}
	if err := patch.Validate(); err != nil {
		return bucket.Info{}, fmt.Errorf("buckets: update %q: %w", name, err)
	}
	release, err := s.lockRegistry(ctx)
	if err != nil {
		return bucket.Info{}, err
	}
	defer release()
	registry, err := s.authoritativeRegistry(ctx)
	if err != nil {
		return bucket.Info{}, err
	}
	current, err := s.Get(ctx, name)
	if err != nil {
		return bucket.Info{}, err
	}
	if patch.IsEmpty() {
		return current, nil
	}
	if patch.DefaultEncryption != nil && *patch.DefaultEncryption == bucket.EncryptionTDE1 {
		if err := s.ensureKeyLocked(ctx, registry, current.ChannelID); err != nil {
			return bucket.Info{}, fmt.Errorf("buckets: update %q: %w", name, err)
		}
	}
	jobID, err := newJobID(s.random)
	if err != nil {
		return bucket.Info{}, err
	}
	_, err = s.publisher.publish(ctx, publication{
		channel: registry,
		op:      s.bucketConfigOp(current, patch, jobID),
		seed:    jobID,
		purpose: purposeBucketConfig,
	})
	if err != nil {
		return bucket.Info{}, fmt.Errorf("buckets: update %q: %w", name, err)
	}
	return s.committedBucket(ctx, current.ChannelID)
}

func (s *Service) Delete(ctx context.Context, name bucket.BucketName) error {
	if err := validateName(name); err != nil {
		return err
	}
	release, err := s.lockRegistry(ctx)
	if err != nil {
		return err
	}
	defer release()
	registry, err := s.authoritativeRegistry(ctx)
	if err != nil {
		return err
	}
	info, err := s.Get(ctx, name)
	if err != nil {
		return err
	}
	if err := s.requireAuthority(ctx, info.ChannelID); err != nil {
		return err
	}
	releaseGate, err := s.writeGates.AcquireExclusive(ctx, info.ChannelID)
	if err != nil {
		return fmt.Errorf("buckets: delete %q: %w", name, err)
	}
	defer releaseGate()
	if err := s.requireEmpty(ctx, info); err != nil {
		return err
	}
	return s.publishDelete(ctx, registry, info)
}

func (s *Service) EnsureKey(ctx context.Context, bucketChannelID int64) error {
	release, err := s.lockRegistry(ctx)
	if err != nil {
		return err
	}
	defer release()
	registry, err := s.authoritativeRegistry(ctx)
	if err != nil {
		return err
	}
	return s.ensureKeyLocked(ctx, registry, bucketChannelID)
}

func (s *Service) RequireWriteAuthority(ctx context.Context, bucketChannelID int64) error {
	if _, err := s.authoritativeRegistry(ctx); err != nil {
		return err
	}
	return s.requireAuthority(ctx, bucketChannelID)
}

func (s *Service) lockRegistry(ctx context.Context) (func(), error) {
	if err := s.registryLock.Acquire(ctx, 1); err != nil {
		return nil, fmt.Errorf("buckets: registry write gate: %w", err)
	}
	return func() { s.registryLock.Release(1) }, nil
}

func (s *Service) authoritativeRegistry(ctx context.Context) (syncer.ChannelState, error) {
	states, err := s.channels.Channels(ctx)
	if err != nil {
		return syncer.ChannelState{}, fmt.Errorf("buckets: list channels: %w", err)
	}
	for _, state := range states {
		if state.Kind == syncer.KindRegistry && !state.Deleted {
			return state, requireAuthoritative(state)
		}
	}
	return syncer.ChannelState{}, fmt.Errorf("buckets: registry channel is not recorded: %w", syncer.ErrNoAuthority)
}

func (s *Service) requireAuthority(ctx context.Context, channelID int64) error {
	state, found, err := s.channels.Channel(ctx, channelID)
	if err != nil {
		return fmt.Errorf("buckets: load channel %d: %w", channelID, err)
	}
	if !found {
		return fmt.Errorf("buckets: channel %d is not recorded: %w", channelID, syncer.ErrNoAuthority)
	}
	return requireAuthoritative(state)
}

func requireAuthoritative(state syncer.ChannelState) error {
	if state.Deleted || !state.InitialSyncDone || state.NeedsProjectionRebuild {
		return fmt.Errorf("buckets: channel %d: %w", state.ChannelID, syncer.ErrNoAuthority)
	}
	return nil
}

func (s *Service) requireRoomForBucket(ctx context.Context, name bucket.BucketName) error {
	if _, found, err := s.buckets.Lookup(ctx, name); err != nil {
		return fmt.Errorf("buckets: lookup %q: %w", name, err)
	} else if found {
		return fmt.Errorf("buckets: create %q: %w", name, errs.ErrBucketExists)
	}
	live, err := s.buckets.ListLive(ctx)
	if err != nil {
		return fmt.Errorf("buckets: list live buckets: %w", err)
	}
	if len(live) >= limits.MaxBuckets {
		return fmt.Errorf("buckets: create %q: %w", name, ErrBucketLimitReached)
	}
	return nil
}

func (s *Service) provisionalOrNewChannel(ctx context.Context, name bucket.BucketName) (syncer.ChannelState, bool, error) {
	existing, found, err := s.findProvisional(ctx, name)
	if err != nil {
		return syncer.ChannelState{}, false, err
	}
	if found {
		return existing, true, nil
	}
	title := bucketChannelTitle + string(name)
	about := fmt.Sprintf("butterDB bucket %s in deployment %s. Do not post, edit or delete messages here.", name, s.deploymentID)
	peer, err := s.creator.CreateChannel(ctx, title, about)
	if err != nil {
		return syncer.ChannelState{}, false, fmt.Errorf("buckets: create channel for %q: %w", name, err)
	}
	row := syncer.ChannelState{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash, Kind: syncer.KindBucket, Title: title}
	if err := s.channels.UpsertChannel(ctx, row); err != nil {
		return syncer.ChannelState{}, false, fmt.Errorf("buckets: record channel %d: %w", peer.ChannelID, err)
	}
	return row, false, nil
}

func (s *Service) findProvisional(ctx context.Context, name bucket.BucketName) (syncer.ChannelState, bool, error) {
	states, err := s.channels.Channels(ctx)
	if err != nil {
		return syncer.ChannelState{}, false, fmt.Errorf("buckets: list channels: %w", err)
	}
	title := bucketChannelTitle + string(name)
	candidates := []syncer.ChannelState{}
	for _, state := range states {
		if state.Kind != syncer.KindBucket || state.Deleted || state.Title != title {
			continue
		}
		if _, committed, err := s.buckets.LookupChannel(ctx, state.ChannelID); err != nil {
			return syncer.ChannelState{}, false, fmt.Errorf("buckets: lookup channel %d: %w", state.ChannelID, err)
		} else if !committed {
			candidates = append(candidates, state)
		}
	}
	if len(candidates) == 0 {
		return syncer.ChannelState{}, false, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ChannelID < candidates[j].ChannelID })
	return s.withUsableAccessHash(ctx, candidates[0])
}

func (s *Service) withUsableAccessHash(ctx context.Context, state syncer.ChannelState) (syncer.ChannelState, bool, error) {
	if state.AccessHash != 0 {
		return state, true, nil
	}
	peer, err := s.publisher.refreshPeer(ctx, state.ChannelID)
	if err != nil {
		return syncer.ChannelState{}, false, err
	}
	state.AccessHash = peer.AccessHash
	return state, true, nil
}

func (s *Service) markEmptyAuthoritative(ctx context.Context, channel syncer.ChannelState) error {
	return markChannelEmptyAuthoritative(ctx, s.channels, s.gate, s.scanner, channel.ChannelID)
}

func (s *Service) committedBucket(ctx context.Context, channelID int64) (bucket.Info, error) {
	info, found, err := s.buckets.LookupChannel(ctx, channelID)
	if err != nil {
		return bucket.Info{}, fmt.Errorf("buckets: lookup channel %d: %w", channelID, err)
	}
	if !found {
		return bucket.Info{}, fmt.Errorf("buckets: channel %d: %w", channelID, ErrOpNotProjected)
	}
	return info, nil
}

func (s *Service) bucketCreateOp(spec bucket.Spec, channelID int64) wire.Op {
	return wire.Op{
		Type:              wire.OpBucketCreate,
		JobID:             derivedCreateJobID(s.deploymentID, channelID),
		Bucket:            string(spec.Name),
		BucketChannelID:   channelID,
		Policy:            spec.Policy,
		DefaultEncryption: spec.DefaultEncryption,
		CacheMaxAge:       spec.CacheMaxAge,
		Writer:            writerBuckets,
		Timestamp:         s.clock.Now().Unix(),
	}
}

func (s *Service) bucketConfigOp(current bucket.Info, patch bucket.Patch, jobID string) wire.Op {
	op := wire.Op{
		Type:              wire.OpBucketConfig,
		JobID:             jobID,
		Bucket:            string(current.Name),
		BucketChannelID:   current.ChannelID,
		Policy:            current.Policy,
		DefaultEncryption: current.DefaultEncryption,
		CacheMaxAge:       current.CacheMaxAge,
		Writer:            writerBuckets,
		Timestamp:         s.clock.Now().Unix(),
	}
	if patch.Policy != nil {
		op.Policy = *patch.Policy
	}
	if patch.DefaultEncryption != nil {
		op.DefaultEncryption = *patch.DefaultEncryption
	}
	if patch.CacheMaxAge != nil {
		op.CacheMaxAge = *patch.CacheMaxAge
	}
	return op
}

func (s *Service) requireEmpty(ctx context.Context, info bucket.Info) error {
	if info.ObjectCount > 0 {
		return fmt.Errorf("buckets: delete %q: %w", info.Name, errs.ErrBucketNotEmpty)
	}
	active, err := s.jobs.HasActiveJobs(ctx, info.ChannelID)
	if err != nil {
		return fmt.Errorf("buckets: delete %q: check jobs: %w", info.Name, err)
	}
	if active {
		return fmt.Errorf("buckets: delete %q: %w", info.Name, errs.ErrBucketNotEmpty)
	}
	return nil
}

func (s *Service) publishDelete(ctx context.Context, registry syncer.ChannelState, info bucket.Info) error {
	jobID, err := newJobID(s.random)
	if err != nil {
		return err
	}
	_, err = s.publisher.publish(ctx, publication{
		channel: registry,
		op: wire.Op{
			Type:            wire.OpBucketDelete,
			JobID:           jobID,
			Bucket:          string(info.Name),
			BucketChannelID: info.ChannelID,
			Writer:          writerBuckets,
			Timestamp:       s.clock.Now().Unix(),
		},
		seed:    jobID,
		purpose: purposeBucketDelete,
	})
	if err != nil {
		return fmt.Errorf("buckets: delete %q: %w", info.Name, err)
	}
	return nil
}

func (s *Service) publishInitialKey(ctx context.Context, registry syncer.ChannelState, bucketChannelID int64) {
	if err := s.publishKey(ctx, registry, bucketChannelID); err != nil {
		s.logger.Warn("buckets: initial encryption key not published; the first encrypted put creates it",
			"bucket_channel_id", bucketChannelID, "error", err)
	}
}

func (s *Service) ensureKeyLocked(ctx context.Context, registry syncer.ChannelState, bucketChannelID int64) error {
	present, err := s.keyPresence.HasActiveKey(ctx, bucketChannelID)
	if err != nil {
		return fmt.Errorf("buckets: check key for channel %d: %w", bucketChannelID, err)
	}
	if present {
		return nil
	}
	return s.publishKey(ctx, registry, bucketChannelID)
}

func (s *Service) publishKey(ctx context.Context, registry syncer.ChannelState, bucketChannelID int64) error {
	op, err := s.keys.MintEncryptionConfig(ctx, bucketChannelID)
	if err != nil {
		return fmt.Errorf("buckets: mint encryption config for channel %d: %w", bucketChannelID, err)
	}
	jobID, err := newJobID(s.random)
	if err != nil {
		return err
	}
	op.JobID = jobID
	op.Writer = writerBuckets
	op.Timestamp = s.clock.Now().Unix()
	_, err = s.publisher.publish(ctx, publication{channel: registry, op: op, seed: jobID, purpose: purposeEncryptionKey})
	return err
}

func validateName(name bucket.BucketName) error {
	if err := name.Validate(); err != nil {
		return fmt.Errorf("buckets: %w: %w", errs.ErrInvalidBucketName, err)
	}
	return nil
}

func validateSpec(spec bucket.Spec) error {
	if err := validateName(spec.Name); err != nil {
		return err
	}
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("buckets: %w", err)
	}
	return nil
}
