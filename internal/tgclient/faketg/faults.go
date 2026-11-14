package faketg

import (
	"fmt"
	"time"

	"github.com/PRIEYAN/ButterDB/internal/tgclient"
)

type Method string

const (
	MethodSendFileIdempotent    Method = "SendFileIdempotent"
	MethodSendControlIdempotent Method = "SendControlIdempotent"
	MethodResolveDocument       Method = "ResolveDocument"
	MethodResolveDocuments      Method = "ResolveDocuments"
	MethodReadDocumentRange     Method = "ReadDocumentRange"
	MethodCreateChannel         Method = "CreateChannel"
	MethodGetHistory            Method = "GetHistory"
	MethodGetChannelDifference  Method = "GetChannelDifference"
	MethodGetChannelPts         Method = "GetChannelPts"
	MethodGetMessages           Method = "GetMessages"
	MethodDeleteMessages        Method = "DeleteMessages"
	MethodListOwnedChannels     Method = "ListOwnedChannels"
	MethodResolveChannel        Method = "ResolveChannel"
	MethodSendToSavedMessages   Method = "SendToSavedMessages"
	MethodSelf                  Method = "Self"
	MethodSendCode              Method = "SendCode"
	MethodSignIn                Method = "SignIn"
	MethodCheckPassword         Method = "CheckPassword"
	MethodGetPasswordHint       Method = "GetPasswordHint"
	MethodLogOut                Method = "LogOut"
)

type FaultKind int

const (
	FaultNone FaultKind = iota
	FaultFloodWait
	FaultAcceptedWithError
	FaultOutcomeUnknown
	FaultTransportDrop
	FaultError
	FaultExpireFileReference
	FaultCDNRedirect
	FaultLatency
	FaultDeleteMessages
	FaultPartialDelete
	FaultDifferenceTooLong
)

type Fault struct {
	Kind       FaultKind
	Latency    time.Duration
	Wait       time.Duration
	Premium    bool
	Err        error
	Delivered  bool
	ChannelID  int64
	MessageIDs []int64
	Count      int
}

func knownMethod(m Method) bool {
	switch m {
	case MethodSendFileIdempotent, MethodSendControlIdempotent, MethodResolveDocument, MethodResolveDocuments,
		MethodReadDocumentRange, MethodCreateChannel, MethodGetHistory, MethodGetChannelDifference,
		MethodGetChannelPts, MethodGetMessages, MethodDeleteMessages, MethodListOwnedChannels,
		MethodResolveChannel, MethodSendToSavedMessages, MethodSelf, MethodSendCode, MethodSignIn,
		MethodCheckPassword, MethodGetPasswordHint, MethodLogOut:
		return true
	}
	return false
}

func kindAppliesTo(kind FaultKind, m Method) bool {
	switch kind {
	case FaultOutcomeUnknown:
		return m == MethodCreateChannel || m == MethodSendFileIdempotent || m == MethodSendControlIdempotent || m == MethodSendToSavedMessages
	case FaultAcceptedWithError:
		return m == MethodSendFileIdempotent || m == MethodSendControlIdempotent || m == MethodSendToSavedMessages
	case FaultExpireFileReference, FaultCDNRedirect:
		return m == MethodReadDocumentRange
	case FaultPartialDelete:
		return m == MethodDeleteMessages
	case FaultDifferenceTooLong:
		return m == MethodGetChannelDifference
	}
	return true
}

func validateFault(m Method, f Fault) error {
	if !knownMethod(m) {
		return fmt.Errorf("faketg: %w: unknown method %q", ErrInvalidFaultScript, m)
	}
	if !kindAppliesTo(f.Kind, m) {
		return fmt.Errorf("faketg: %w: fault kind %d does not apply to %s", ErrInvalidFaultScript, f.Kind, m)
	}
	if f.Latency < 0 {
		return fmt.Errorf("faketg: %w: negative latency", ErrInvalidFaultScript)
	}
	switch f.Kind {
	case FaultFloodWait:
		if f.Wait <= 0 {
			return fmt.Errorf("faketg: %w: flood wait needs a positive duration", ErrInvalidFaultScript)
		}
	case FaultError:
		if f.Err == nil {
			return fmt.Errorf("faketg: %w: error fault needs an error", ErrInvalidFaultScript)
		}
	case FaultLatency:
		if f.Latency <= 0 {
			return fmt.Errorf("faketg: %w: latency fault needs a positive latency", ErrInvalidFaultScript)
		}
	case FaultDeleteMessages:
		if len(f.MessageIDs) == 0 {
			return fmt.Errorf("faketg: %w: delete fault needs message ids", ErrInvalidFaultScript)
		}
	case FaultPartialDelete:
		if f.Count < 0 {
			return fmt.Errorf("faketg: %w: negative partial delete count", ErrInvalidFaultScript)
		}
	}
	return nil
}

func (f Fault) immediateError() error {
	switch f.Kind {
	case FaultFloodWait:
		flood := &tgclient.FloodWaitError{Duration: f.Wait}
		if f.Premium {
			return fmt.Errorf("faketg: FLOOD_PREMIUM_WAIT_%d: %w", int64(f.Wait/time.Second), flood)
		}
		return fmt.Errorf("faketg: FLOOD_WAIT_%d: %w", int64(f.Wait/time.Second), flood)
	case FaultTransportDrop:
		return ErrInjectedTransport
	case FaultError:
		return f.Err
	}
	return nil
}

func (f Fault) sendFailure() error {
	if f.Kind == FaultOutcomeUnknown {
		return fmt.Errorf("faketg: %w", tgclient.ErrSendOutcomeUnknown)
	}
	if f.Kind == FaultAcceptedWithError {
		if f.Err != nil {
			return f.Err
		}
		return ErrInjectedAccepted
	}
	return nil
}

type script struct {
	queues  map[Method][]Fault
	onCall  map[Method]map[int]Fault
	calls   map[Method]int
	latency map[Method]time.Duration
}

func newScript() script {
	return script{
		queues:  make(map[Method][]Fault),
		onCall:  make(map[Method]map[int]Fault),
		calls:   make(map[Method]int),
		latency: make(map[Method]time.Duration),
	}
}

func (s *script) next(m Method) Fault {
	s.calls[m]++
	if f, ok := s.onCall[m][s.calls[m]]; ok {
		return f
	}
	queue := s.queues[m]
	if len(queue) == 0 {
		return Fault{}
	}
	s.queues[m] = queue[1:]
	return queue[0]
}

func (c *Client) QueueFaults(m Method, faults ...Fault) error {
	for _, f := range faults {
		if err := validateFault(m, f); err != nil {
			return err
		}
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	c.script.queues[m] = append(c.script.queues[m], faults...)
	return nil
}

func (c *Client) FaultOnCall(m Method, callNumber int, f Fault) error {
	if err := validateFault(m, f); err != nil {
		return err
	}
	if callNumber <= 0 {
		return fmt.Errorf("faketg: %w: call number must be positive", ErrInvalidFaultScript)
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	if c.script.onCall[m] == nil {
		c.script.onCall[m] = make(map[int]Fault)
	}
	c.script.onCall[m][callNumber] = f
	return nil
}

func (c *Client) SetLatency(m Method, d time.Duration) error {
	if !knownMethod(m) || d < 0 {
		return fmt.Errorf("faketg: %w: latency for %q", ErrInvalidFaultScript, m)
	}
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	c.script.latency[m] = d
	return nil
}

func (c *Client) CallCount(m Method) int {
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	return c.script.calls[m]
}

func (c *Client) ClearFaults() {
	c.n.mu.Lock()
	defer c.n.mu.Unlock()
	c.script.queues = make(map[Method][]Fault)
	c.script.onCall = make(map[Method]map[int]Fault)
	c.script.latency = make(map[Method]time.Duration)
}
