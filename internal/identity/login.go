package identity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/domain/errs"
	"github.com/PRIEYAN/ButterDB/internal/platform/clock"
	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

const (
	DefaultStepTimeout  = 60 * time.Second
	DefaultAttemptTTL   = 15 * time.Minute
	MaxCodeAttempts     = 5
	MaxPasswordAttempts = 5
	minCodeLength       = 1
	maxCodeLength       = 16
	loginIDPrefix       = "lg_"
	loginIDLength       = 12
	loginIDAlphabet     = "abcdefghijklmnopqrstuvwxyz0123456789"
	loginIDRejectFrom   = 252
	deliveryTypeOther   = "other"
)

type loginStage int

const (
	stageIdle loginStage = iota
	stageStarted
	stageWaitingCode
	stageWaitingPassword
)

type Dependencies struct {
	Role            Role
	CredentialsPath string
	Writer          SecretWriter
	Reader          FileReader
	Remover         FileRemover
	Auth            AuthRPC
	Self            SelfReader
	Accounts        AccountStore
	Sessions        SessionDiscarder
	Clock           clock.Clock
	Random          io.Reader
	Logger          *slog.Logger
	StepTimeout     time.Duration
	AttemptTTL      time.Duration
}

type attempt struct {
	id           string
	phone        string
	phoneMasked  string
	codeHash     string
	delivery     CodeDelivery
	hint         string
	stage        loginStage
	busy         bool
	codeLeft     int
	passwordLeft int
	startedAt    time.Time
	expiresAt    time.Time
	cancelStep   context.CancelFunc
}

type Service struct {
	role            Role
	credentialsPath string
	writer          SecretWriter
	reader          FileReader
	remover         FileRemover
	auth            AuthRPC
	self            SelfReader
	accounts        AccountStore
	sessions        SessionDiscarder
	clock           clock.Clock
	log             *slog.Logger
	stepTimeout     time.Duration
	attemptTTL      time.Duration

	randMu sync.Mutex
	random io.Reader

	mu        sync.Mutex
	current   *attempt
	lastError *Failure
	firstName string
}

func NewService(deps Dependencies) (*Service, error) {
	if err := validateDependencies(deps); err != nil {
		return nil, err
	}
	s := &Service{
		role:            deps.Role,
		credentialsPath: deps.CredentialsPath,
		writer:          deps.Writer,
		reader:          deps.Reader,
		remover:         deps.Remover,
		auth:            deps.Auth,
		self:            deps.Self,
		accounts:        deps.Accounts,
		sessions:        deps.Sessions,
		clock:           deps.Clock,
		log:             deps.Logger,
		random:          deps.Random,
		stepTimeout:     deps.StepTimeout,
		attemptTTL:      deps.AttemptTTL,
	}
	if s.stepTimeout <= 0 {
		s.stepTimeout = DefaultStepTimeout
	}
	if s.attemptTTL <= 0 {
		s.attemptTTL = DefaultAttemptTTL
	}
	return s, nil
}

func validateDependencies(deps Dependencies) error {
	missing := []struct {
		name string
		nil  bool
	}{
		{"writer", deps.Writer == nil},
		{"reader", deps.Reader == nil},
		{"remover", deps.Remover == nil},
		{"auth", deps.Auth == nil},
		{"self", deps.Self == nil},
		{"accounts", deps.Accounts == nil},
		{"sessions", deps.Sessions == nil},
		{"clock", deps.Clock == nil},
		{"random", deps.Random == nil},
		{"logger", deps.Logger == nil},
	}
	for _, dep := range missing {
		if dep.nil {
			return fmt.Errorf("identity: %w: %s", errs.ErrMissingDependency, dep.name)
		}
	}
	if !deps.Role.valid() {
		return fmt.Errorf("identity: invalid role %q", string(deps.Role))
	}
	if deps.CredentialsPath == "" {
		return fmt.Errorf("identity: %w: credentials path", errs.ErrMissingDependency)
	}
	return nil
}

func (s *Service) Role() Role {
	return s.role
}

func (s *Service) Status(ctx context.Context) (Snapshot, error) {
	return s.snapshot(ctx)
}

func (s *Service) Linked(ctx context.Context) (bool, error) {
	return s.isLinked(ctx)
}

func (s *Service) snapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	var snap Snapshot
	creds, err := s.LoadCredentials(ctx)
	credentialsSet := err == nil
	if credentialsSet {
		snap.Credentials = CredentialsSummary{APIID: creds.APIID, APIHashSet: true}
	}
	if err != nil && !errors.Is(err, ErrCredentialsMissing) && !errors.Is(err, ErrCredentialsCorrupt) {
		return Snapshot{}, err
	}
	credentialsErr := err
	row, found, err := s.accounts.Get(ctx, string(s.role))
	if err != nil {
		return Snapshot{}, fmt.Errorf("identity: status: %w", err)
	}
	if errors.Is(credentialsErr, ErrCredentialsCorrupt) && !(found && row.Linked) {
		return Snapshot{}, credentialsErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	if s.lastError != nil {
		failure := *s.lastError
		snap.LastError = &failure
	}
	switch {
	case found && row.Linked:
		snap.State = StateLinked
		snap.Account = &AccountSummary{
			UserID:      row.UserID,
			Username:    row.Username,
			FirstName:   s.firstName,
			PhoneMasked: row.PhoneMasked,
			LinkedAt:    time.UnixMilli(row.LinkedAt).UTC(),
		}
	case s.current != nil:
		snap.State, snap.Login = s.current.view()
	case credentialsSet:
		snap.State = StateReady
	default:
		snap.State = StateNoCredentials
	}
	return snap, nil
}

func (a *attempt) view() (State, *LoginSummary) {
	state := StateSendingCode
	switch a.stage {
	case stageWaitingCode:
		state = StateAwaitingCode
	case stageWaitingPassword:
		state = StateAwaitingPassword
	}
	login := &LoginSummary{
		LoginID:              a.id,
		PhoneMasked:          a.phoneMasked,
		CodeDelivery:         a.delivery,
		CodeAttemptsLeft:     a.codeLeft,
		PasswordAttemptsLeft: a.passwordLeft,
		StartedAt:            a.startedAt,
		ExpiresAt:            a.expiresAt,
	}
	if a.stage == stageWaitingPassword {
		login.PasswordHint = a.hint
	}
	return state, login
}

func (s *Service) isLinked(ctx context.Context) (bool, error) {
	row, found, err := s.accounts.Get(ctx, string(s.role))
	if err != nil {
		return false, fmt.Errorf("identity: read account binding: %w", err)
	}
	return found && row.Linked, nil
}

func (s *Service) attemptActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	return s.current != nil
}

func (s *Service) expireLocked() {
	a := s.current
	if a == nil || s.clock.Now().Before(a.expiresAt) {
		return
	}
	if a.cancelStep != nil {
		a.cancelStep()
	}
	s.current = nil
}

func (s *Service) accepts(a *attempt, stages ...loginStage) bool {
	if s.current != a || a == nil {
		return false
	}
	for _, stage := range stages {
		if a.stage == stage {
			return true
		}
	}
	return false
}

func (s *Service) beginStepLocked(ctx context.Context, a *attempt) (context.Context, func()) {
	stepCtx, cancel := context.WithTimeout(ctx, s.stepTimeout)
	a.busy = true
	a.cancelStep = cancel
	return stepCtx, func() {
		s.mu.Lock()
		a.busy = false
		a.cancelStep = nil
		s.mu.Unlock()
		cancel()
	}
}

func (s *Service) claim(ctx context.Context, loginID string, stage loginStage) (*attempt, context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	a := s.current
	if a == nil || a.id != loginID || a.stage != stage || a.busy {
		return nil, nil, nil, fmt.Errorf("identity: submit: %w", ErrLoginNotActive)
	}
	stepCtx, release := s.beginStepLocked(ctx, a)
	return a, stepCtx, release, nil
}

func (s *Service) Start(ctx context.Context, rawPhone string) (Snapshot, error) {
	phone, err := NormalizePhone(rawPhone)
	if err != nil {
		return s.withSnapshot(ctx, fmt.Errorf("identity: start: %w", err))
	}
	if _, err := s.LoadCredentials(ctx); err != nil {
		return s.withSnapshot(ctx, credentialsStartError(err))
	}
	linked, err := s.isLinked(ctx)
	if err != nil {
		return s.withSnapshot(ctx, err)
	}
	if linked {
		return s.withSnapshot(ctx, fmt.Errorf("identity: start: %w", ErrAlreadyLinked))
	}
	loginID, err := s.newLoginID()
	if err != nil {
		return s.withSnapshot(ctx, err)
	}
	a, stepCtx, release, err := s.openAttempt(ctx, loginID, phone)
	if err != nil {
		return s.withSnapshot(ctx, err)
	}
	defer release()
	var sent tgclient.SentCode
	rpcErr := s.guard(func() error {
		var callErr error
		sent, callErr = s.auth.SendCode(stepCtx, phone)
		return callErr
	})
	if rpcErr != nil {
		return s.withSnapshot(ctx, s.terminate(a, s.classifyStepError(ctx, stepCtx, a, rpcErr).err))
	}
	if err := s.recordSentCode(a, sent); err != nil {
		return s.withSnapshot(ctx, err)
	}
	return s.snapshot(ctx)
}

func credentialsStartError(err error) error {
	if errors.Is(err, ErrCredentialsMissing) {
		return fmt.Errorf("identity: start: %w", ErrCredentialsMissing)
	}
	return err
}

func (s *Service) openAttempt(ctx context.Context, loginID, phone string) (*attempt, context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	if s.current != nil {
		return nil, nil, nil, fmt.Errorf("identity: start: %w", ErrLoginInProgress)
	}
	now := s.clock.Now()
	a := &attempt{
		id:           loginID,
		phone:        phone,
		phoneMasked:  MaskPhone(phone),
		stage:        stageStarted,
		codeLeft:     MaxCodeAttempts,
		passwordLeft: MaxPasswordAttempts,
		startedAt:    now.UTC(),
		expiresAt:    now.Add(s.attemptTTL).UTC(),
	}
	s.current = a
	s.lastError = nil
	stepCtx, release := s.beginStepLocked(ctx, a)
	return a, stepCtx, release, nil
}

func (s *Service) recordSentCode(a *attempt, sent tgclient.SentCode) error {
	if sent.PhoneCodeHash == "" {
		return s.terminate(a, &StepError{Err: ErrLoginFailed})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepts(a, stageStarted) {
		return fmt.Errorf("identity: start: %w", ErrLoginNotActive)
	}
	a.stage = stageWaitingCode
	a.codeHash = sent.PhoneCodeHash
	a.delivery = CodeDelivery{Type: deliveryTypeOther, TimeoutSeconds: int(sent.Timeout / time.Second)}
	return nil
}

func (s *Service) SubmitCode(ctx context.Context, loginID, code string) (Snapshot, error) {
	a, stepCtx, release, err := s.claim(ctx, loginID, stageWaitingCode)
	if err != nil {
		return s.withSnapshot(ctx, err)
	}
	defer release()
	code = strings.TrimSpace(code)
	if len(code) < minCodeLength || len(code) > maxCodeLength {
		return s.withSnapshot(ctx, s.rejectCode(a, false))
	}
	var account tgclient.Account
	rpcErr := s.guard(func() error {
		var callErr error
		account, callErr = s.auth.SignIn(stepCtx, a.phone, a.codeHash, code)
		return callErr
	})
	switch {
	case rpcErr == nil:
		return s.finishLinked(ctx, stepCtx, a, account)
	case errors.Is(rpcErr, tgclient.ErrPasswordNeeded):
		return s.awaitPassword(ctx, stepCtx, a)
	case errors.Is(rpcErr, tgclient.ErrPhoneCodeInvalid):
		return s.withSnapshot(ctx, s.rejectCode(a, true))
	case errors.Is(rpcErr, tgclient.ErrPhoneCodeExpired):
		return s.withSnapshot(ctx, s.terminate(a, &StepError{Err: ErrCodeExpired}))
	}
	return s.withSnapshot(ctx, s.settleFailure(ctx, stepCtx, a, rpcErr))
}

func (s *Service) SubmitPassword(ctx context.Context, loginID, password string) (Snapshot, error) {
	a, stepCtx, release, err := s.claim(ctx, loginID, stageWaitingPassword)
	if err != nil {
		return s.withSnapshot(ctx, err)
	}
	defer release()
	var account tgclient.Account
	rpcErr := s.guard(func() error {
		var callErr error
		account, callErr = s.auth.CheckPassword(stepCtx, password)
		return callErr
	})
	switch {
	case rpcErr == nil:
		return s.finishLinked(ctx, stepCtx, a, account)
	case errors.Is(rpcErr, tgclient.ErrPasswordInvalid):
		return s.withSnapshot(ctx, s.rejectPassword(a))
	}
	return s.withSnapshot(ctx, s.settleFailure(ctx, stepCtx, a, rpcErr))
}

func (s *Service) rejectCode(a *attempt, consume bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepts(a, stageWaitingCode) {
		return fmt.Errorf("identity: submit code: %w", ErrLoginNotActive)
	}
	if consume {
		a.codeLeft--
	}
	if a.codeLeft <= 0 {
		return s.terminateLocked(a, &StepError{Err: ErrAttemptsExhausted})
	}
	return &StepError{Err: ErrCodeInvalid, AttemptsLeft: a.codeLeft}
}

func (s *Service) rejectPassword(a *attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.accepts(a, stageWaitingPassword) {
		return fmt.Errorf("identity: submit password: %w", ErrLoginNotActive)
	}
	a.passwordLeft--
	if a.passwordLeft <= 0 {
		return s.terminateLocked(a, &StepError{Err: ErrAttemptsExhausted})
	}
	return &StepError{Err: ErrPasswordInvalid, AttemptsLeft: a.passwordLeft}
}

func (s *Service) awaitPassword(ctx, stepCtx context.Context, a *attempt) (Snapshot, error) {
	hint := s.passwordHint(stepCtx)
	s.mu.Lock()
	if !s.accepts(a, stageWaitingCode) {
		s.mu.Unlock()
		return s.withSnapshot(ctx, fmt.Errorf("identity: submit code: %w", ErrLoginNotActive))
	}
	a.stage = stageWaitingPassword
	a.hint = hint
	s.mu.Unlock()
	return s.snapshot(ctx)
}

func (s *Service) passwordHint(ctx context.Context) (hint string) {
	defer func() {
		if recover() != nil {
			hint = ""
		}
	}()
	value, err := s.auth.GetPasswordHint(ctx)
	if err != nil {
		s.log.Warn("identity: password hint lookup failed")
		return ""
	}
	return value
}

func (s *Service) finishLinked(ctx, stepCtx context.Context, a *attempt, account tgclient.Account) (Snapshot, error) {
	if account.UserID <= 0 {
		resolved, err := s.resolveSelf(stepCtx)
		if err != nil {
			return s.withSnapshot(ctx, s.settleFailure(ctx, stepCtx, a, err))
		}
		account = resolved
	}
	if err := s.bindAccount(ctx, a, account); err != nil {
		return s.withSnapshot(ctx, s.rejectBinding(ctx, a, err))
	}
	s.mu.Lock()
	if s.current == a {
		s.current = nil
	}
	s.lastError = nil
	s.firstName = account.FirstName
	s.mu.Unlock()
	return s.snapshot(ctx)
}

func (s *Service) resolveSelf(ctx context.Context) (account tgclient.Account, err error) {
	err = s.guard(func() error {
		var callErr error
		account, callErr = s.self.Self(ctx)
		return callErr
	})
	if err == nil && account.UserID <= 0 {
		err = fmt.Errorf("identity: resolve self: %w", ErrLoginFailed)
	}
	return account, err
}

func (s *Service) bindAccount(ctx context.Context, a *attempt, account tgclient.Account) error {
	row, found, err := s.accounts.Get(ctx, string(s.role))
	if err != nil {
		return fmt.Errorf("identity: read account binding: %w", err)
	}
	if found && row.UserID != account.UserID {
		return &StepError{Err: ErrAccountMismatch, BoundUserID: row.UserID}
	}
	otherRole, taken, err := s.accounts.FindByUser(ctx, account.UserID)
	if err != nil {
		return fmt.Errorf("identity: read account binding: %w", err)
	}
	if taken && otherRole != string(s.role) {
		return &StepError{Err: ErrReplicaSameAccount}
	}
	now := s.clock.Now().UnixMilli()
	linkErr := s.accounts.Link(ctx, StoredAccount{
		Role:        string(s.role),
		UserID:      account.UserID,
		Username:    account.Username,
		PhoneMasked: a.phoneMasked,
		Linked:      true,
		BoundAt:     now,
		LinkedAt:    now,
	})
	if linkErr != nil {
		return fmt.Errorf("identity: store account binding: %w", linkErr)
	}
	return nil
}

func (s *Service) rejectBinding(ctx context.Context, a *attempt, err error) error {
	var step *StepError
	if errors.As(err, &step) {
		s.discardAuthorization(ctx)
		return s.terminate(a, err)
	}
	return s.terminate(a, &StepError{Err: ErrLoginFailed, Cause: err})
}

func (s *Service) discardAuthorization(ctx context.Context) {
	logoutCtx, cancel := context.WithTimeout(ctx, s.stepTimeout)
	defer cancel()
	if err := s.guard(func() error { return s.auth.LogOut(logoutCtx) }); err != nil {
		s.log.Warn("identity: logout after rejected login failed")
	}
	if err := s.sessions.Delete(ctx); err != nil {
		s.log.Warn("identity: discarding rejected session failed")
	}
}

type stepOutcome struct {
	err  error
	keep bool
}

func (s *Service) settleFailure(ctx, stepCtx context.Context, a *attempt, rpcErr error) error {
	outcome := s.classifyStepError(ctx, stepCtx, a, rpcErr)
	if outcome.keep {
		return outcome.err
	}
	return s.terminate(a, outcome.err)
}

func (s *Service) classifyStepError(ctx, stepCtx context.Context, a *attempt, rpcErr error) stepOutcome {
	s.mu.Lock()
	stillCurrent := s.current == a
	s.mu.Unlock()
	if !stillCurrent {
		return stepOutcome{err: fmt.Errorf("identity: step: %w", ErrLoginNotActive), keep: true}
	}
	var wait *tgclient.FloodWaitError
	switch {
	case errors.As(rpcErr, &wait):
		return stepOutcome{err: fmt.Errorf("identity: step: %w", rpcErr), keep: true}
	case ctx.Err() != nil:
		return stepOutcome{err: fmt.Errorf("identity: step: %w", ctx.Err()), keep: true}
	case errors.Is(stepCtx.Err(), context.DeadlineExceeded):
		return stepOutcome{err: fmt.Errorf("identity: step: %w", ErrTelegramUnavailable), keep: true}
	}
	return stepOutcome{err: &StepError{Err: ErrLoginFailed, Cause: rpcErr}}
}

func (s *Service) terminate(a *attempt, err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminateLocked(a, err)
}

func (s *Service) terminateLocked(a *attempt, err error) error {
	if s.current != a {
		return err
	}
	if errors.Is(err, ErrLoginNotActive) {
		return err
	}
	failure := FailureOf(err)
	s.lastError = &failure
	s.current = nil
	return err
}

func (s *Service) Cancel(ctx context.Context) (Snapshot, error) {
	s.mu.Lock()
	if a := s.current; a != nil {
		if a.cancelStep != nil {
			a.cancelStep()
		}
		s.current = nil
	}
	s.mu.Unlock()
	return s.snapshot(ctx)
}

func (s *Service) Unlink(ctx context.Context) (Snapshot, error) {
	row, found, err := s.accounts.Get(ctx, string(s.role))
	if err != nil {
		return s.withSnapshot(ctx, fmt.Errorf("identity: unlink: %w", err))
	}
	if !found || !row.Linked {
		return s.snapshot(ctx)
	}
	logoutCtx, cancel := context.WithTimeout(ctx, s.stepTimeout)
	logoutErr := s.guard(func() error { return s.auth.LogOut(logoutCtx) })
	cancel()
	if err := s.sessions.Delete(ctx); err != nil {
		return s.withSnapshot(ctx, fmt.Errorf("identity: unlink: delete session: %w", err))
	}
	if err := s.accounts.Unlink(ctx, string(s.role), s.clock.Now().UnixMilli()); err != nil {
		return s.withSnapshot(ctx, fmt.Errorf("identity: unlink: %w", err))
	}
	s.mu.Lock()
	s.lastError = nil
	if logoutErr != nil {
		s.log.Warn("identity: logout during unlink failed")
		s.lastError = &Failure{Code: CodeUnavailable}
	}
	s.firstName = ""
	s.mu.Unlock()
	return s.snapshot(ctx)
}

func (s *Service) withSnapshot(ctx context.Context, err error) (Snapshot, error) {
	snap, _ := s.snapshot(ctx)
	return snap, err
}

func (s *Service) guard(fn func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.log.Error("identity: login step panicked")
			err = &StepError{Err: ErrLoginFailed}
		}
	}()
	return fn()
}

func (s *Service) newLoginID() (string, error) {
	out := make([]byte, 0, loginIDLength)
	buf := make([]byte, loginIDLength)
	s.randMu.Lock()
	defer s.randMu.Unlock()
	for len(out) < loginIDLength {
		if _, err := io.ReadFull(s.random, buf); err != nil {
			return "", fmt.Errorf("identity: login id: %w", err)
		}
		for _, b := range buf {
			if b < loginIDRejectFrom && len(out) < loginIDLength {
				out = append(out, loginIDAlphabet[int(b)%len(loginIDAlphabet)])
			}
		}
	}
	return loginIDPrefix + string(out), nil
}
