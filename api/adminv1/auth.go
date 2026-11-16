package adminv1

import (
	"time"
)

type ServerInfo struct {
	ServerID       string    `json:"server_id"`
	APIVersion     string    `json:"api_version"`
	ServerVersion  string    `json:"server_version"`
	State          string    `json:"state"`
	TelegramLinked bool      `json:"telegram_linked"`
	PairingMethods []string  `json:"pairing_methods"`
	TLS            TLSInfo   `json:"tls"`
	Time           time.Time `json:"time"`
}

type TLSInfo struct {
	Mode       string `json:"mode"`
	SPKISHA256 string `json:"spki_sha256,omitempty"`
}

type ClaimRequest struct {
	SetupToken    string `json:"setup_token"`
	DeviceName    string `json:"device_name"`
	Platform      string `json:"platform"`
	ClientVersion string `json:"client_version"`
}

type PairingRequest struct {
	Delivery   string `json:"delivery"`
	DeviceName string `json:"device_name"`
	Platform   string `json:"platform"`
}

type PairingRequested struct {
	Delivery  string    `json:"delivery"`
	ExpiresAt time.Time `json:"expires_at"`
}

type PairingCompleteRequest struct {
	Code          string `json:"code"`
	DeviceName    string `json:"device_name"`
	Platform      string `json:"platform"`
	ClientVersion string `json:"client_version"`
}

type Device struct {
	DeviceID      string     `json:"device_id"`
	Name          string     `json:"name"`
	Platform      string     `json:"platform"`
	CreatedAt     time.Time  `json:"created_at"`
	CreatedVia    string     `json:"created_via"`
	LastSeenAt    time.Time  `json:"last_seen_at"`
	LastSeenIP    string     `json:"last_seen_ip"`
	TokenIssuedAt time.Time  `json:"token_issued_at"`
	RevokedAt     *time.Time `json:"revoked_at"`
	Current       bool       `json:"current"`
}

type DeviceCredentials struct {
	Device      Device `json:"device"`
	DeviceToken string `json:"device_token"`
	ServerID    string `json:"server_id"`
}

type DeviceList struct {
	Devices []Device `json:"devices"`
}

type UpdateDeviceRequest struct {
	Name string `json:"name"`
}

type UnlinkRequest struct {
	Confirm string `json:"confirm"`
}
