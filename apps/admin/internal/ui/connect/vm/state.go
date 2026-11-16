package vm

import (
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type Step string

const (
	StepProfiles           Step = "profiles"
	StepConfirmCertificate Step = "confirm_certificate"
	StepClaim              Step = "claim"
	StepPair               Step = "pair"
	StepStorageChoice      Step = "storage_choice"
	StepPinMismatch        Step = "pin_mismatch"
)

type ProfileRow struct {
	ServerID  string
	Label     string
	URL       string
	LastUsed  time.Time
	TrustMode adminv1.TrustMode
	Active    bool
}

type CertificateView struct {
	Fingerprint             string
	Subject                 string
	DNSNames                []string
	IPAddresses             []string
	NotAfter                time.Time
	VerifiesWithSystemRoots bool
}

type MismatchView struct {
	ServerLabel string
	Expected    string
	Received    string
}

type State struct {
	Loading              bool
	Problem              *model.Problem
	Notice               string
	Step                 Step
	Profiles             []ProfileRow
	URL                  string
	PlainHTTP            bool
	Rebinding            bool
	ServerLabel          string
	Certificate          *CertificateView
	SetupToken           string
	PairingCode          string
	PairingCodeComplete  bool
	SavedMessagesEnabled bool
	PairingRequested     bool
	PairingExpiresAt     time.Time
	DefaultDeviceName    string
	Mismatch             *MismatchView
}
