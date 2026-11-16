package adminrepo

import (
	"time"
)

type CreatedVia string

const (
	CreatedViaSetup                CreatedVia = "setup"
	CreatedViaPairingSavedMessages CreatedVia = "pairing_saved_messages"
	CreatedViaPairingHost          CreatedVia = "pairing_host"
)

type NewDevice struct {
	DeviceID      string
	Name          string
	Platform      string
	ClientVersion string
	CreatedVia    CreatedVia
	TokenHash     [32]byte
	CreatedAt     time.Time
	LastSeenIP    string
}

type Device struct {
	DeviceID       string
	Name           string
	Platform       string
	ClientVersion  string
	CreatedVia     CreatedVia
	TokenHash      [32]byte
	TokenIssuedAt  time.Time
	PrevTokenHash  [32]byte
	HasPrevToken   bool
	PrevValidUntil time.Time
	CreatedAt      time.Time
	LastSeenAt     time.Time
	LastSeenIP     string
	RevokedAt      time.Time
	RevokedBy      string
}
