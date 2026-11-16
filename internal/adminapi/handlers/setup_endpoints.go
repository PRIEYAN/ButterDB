package handlers

import (
	"net/http"
	"strings"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/internal/storage/adminrepo"
)

const (
	pairingDeliverySavedMessages = "saved_messages"
	maxTokenChars                = 256
)

func (a *api) registerSetupRoutes() {
	a.public("GET "+adminv1.PathServerInfo, a.serverInfo)
	a.public("POST "+adminv1.PathSetupClaim, a.claim)
	a.publicRelay("POST "+adminv1.PathPairingRequest, a.pairingRequest)
	a.public("POST "+adminv1.PathPairingComplete, a.pairingComplete)
	a.authed("GET "+adminv1.PathDevices, a.listDevices)
	a.authed("PATCH "+adminv1.PathDevice, a.renameDevice)
	a.authed("DELETE "+adminv1.PathDevice, a.revokeDevice)
	a.authed("POST "+adminv1.PathDeviceSelfRotate, a.rotateDevice)
}

func (a *api) currentDevice(r *http.Request) (adminrepo.Device, error) {
	device, ok := a.deps.Device(r.Context())
	if !ok {
		return adminrepo.Device{}, failure(adminv1.CodeUnauthenticated, nil)
	}
	return device, nil
}

func (a *api) deviceInfo(r *http.Request, name, platform, clientVersion string) adminv1.DeviceInfo {
	return adminv1.DeviceInfo{Name: name, Platform: platform, ClientVersion: clientVersion, RemoteAddr: a.clientAddress(r)}
}

func (a *api) serverInfo(w http.ResponseWriter, r *http.Request) error {
	info, err := a.deps.Setup.ServerInfo(r.Context())
	if err != nil {
		return err
	}
	if info.Time.IsZero() {
		info.Time = a.deps.Clock.Now()
	}
	info.Time = utcMillis(info.Time)
	if info.PairingMethods == nil {
		info.PairingMethods = []string{}
	}
	a.writeJSON(w, r, http.StatusOK, info)
	return nil
}

func (a *api) claim(w http.ResponseWriter, r *http.Request) error {
	var req adminv1.ClaimRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	token := strings.TrimSpace(req.SetupToken)
	if token == "" || len(token) > maxTokenChars {
		return &ValidationError{Field: "setup_token", Reason: "length out of range"}
	}
	if err := validateDeviceFields(req.DeviceName, req.Platform, req.ClientVersion); err != nil {
		return err
	}
	result, err := a.deps.Setup.Claim(r.Context(), token, a.deviceInfo(r, req.DeviceName, req.Platform, req.ClientVersion))
	if err != nil {
		a.recordAs(r, adminv1.AuditActor{Type: actorAnonymous}, "setup.claim", "", err)
		return err
	}
	actor := adminv1.AuditActor{Type: actorDevice, DeviceID: result.Device.DeviceID, DeviceName: result.Device.Name}
	a.recordAs(r, actor, "setup.claim", result.Device.DeviceID, nil)
	a.writeJSON(w, r, http.StatusCreated, result)
	return nil
}

func (a *api) pairingRequest(w http.ResponseWriter, r *http.Request) error {
	var req adminv1.PairingRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	if req.Delivery != pairingDeliverySavedMessages {
		return &ValidationError{Field: "delivery", Reason: "must be saved_messages"}
	}
	if err := validateDeviceFields(req.DeviceName, req.Platform, ""); err != nil {
		return err
	}
	expiresAt, err := a.deps.Pairing.Request(r.Context(), req.Delivery, a.deviceInfo(r, req.DeviceName, req.Platform, ""))
	a.recordAs(r, adminv1.AuditActor{Type: actorAnonymous}, "pairing.request", req.Delivery, err)
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusAccepted, adminv1.PairingRequested{Delivery: req.Delivery, ExpiresAt: utcMillis(expiresAt)})
	return nil
}

func (a *api) pairingComplete(w http.ResponseWriter, r *http.Request) error {
	var req adminv1.PairingCompleteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	code := strings.TrimSpace(req.Code)
	if code == "" || len(code) > maxTokenChars {
		return &ValidationError{Field: "code", Reason: "length out of range"}
	}
	if err := validateDeviceFields(req.DeviceName, req.Platform, req.ClientVersion); err != nil {
		return err
	}
	result, err := a.deps.Pairing.Complete(r.Context(), code, a.deviceInfo(r, req.DeviceName, req.Platform, req.ClientVersion))
	if err != nil {
		a.recordAs(r, adminv1.AuditActor{Type: actorAnonymous}, "pairing.complete", "", err)
		return err
	}
	actor := adminv1.AuditActor{Type: actorDevice, DeviceID: result.Device.DeviceID, DeviceName: result.Device.Name}
	a.recordAs(r, actor, "pairing.complete", result.Device.DeviceID, nil)
	a.writeJSON(w, r, http.StatusCreated, result)
	return nil
}

func (a *api) listDevices(w http.ResponseWriter, r *http.Request) error {
	current, err := a.currentDevice(r)
	if err != nil {
		return err
	}
	devices, err := a.deps.Devices.List(r.Context())
	if err != nil {
		return err
	}
	list := adminv1.DeviceList{Devices: make([]adminv1.Device, 0, len(devices))}
	for _, device := range devices {
		list.Devices = append(list.Devices, deviceDTO(device, current.DeviceID))
	}
	a.writeJSON(w, r, http.StatusOK, list)
	return nil
}

func (a *api) renameDevice(w http.ResponseWriter, r *http.Request) error {
	current, err := a.currentDevice(r)
	if err != nil {
		return err
	}
	var req adminv1.UpdateDeviceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		return err
	}
	if err := validateText("name", req.Name, 1, maxDeviceNameRunes); err != nil {
		return err
	}
	id := r.PathValue(adminv1.ParamDeviceID)
	device, err := a.deps.Devices.Rename(r.Context(), id, req.Name)
	a.record(r, "device.rename", id, err)
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusOK, deviceDTO(device, current.DeviceID))
	return nil
}

func (a *api) revokeDevice(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.currentDevice(r); err != nil {
		return err
	}
	confirmLast, err := parseBoolQuery(r.URL.Query().Get(adminv1.QueryConfirmLast))
	if err != nil {
		return &BadRequestError{Field: adminv1.QueryConfirmLast}
	}
	id := r.PathValue(adminv1.ParamDeviceID)
	err = a.deps.Devices.Revoke(r.Context(), id, confirmLast)
	a.record(r, "device.revoke", id, err)
	if err != nil {
		return err
	}
	a.writeNoContent(w)
	return nil
}

func (a *api) rotateDevice(w http.ResponseWriter, r *http.Request) error {
	current, err := a.currentDevice(r)
	if err != nil {
		return err
	}
	device, token, err := a.deps.Devices.Rotate(r.Context(), current.DeviceID)
	a.record(r, "device.rotate", current.DeviceID, err)
	if err != nil {
		return err
	}
	info, err := a.deps.Setup.ServerInfo(r.Context())
	if err != nil {
		return err
	}
	a.writeJSON(w, r, http.StatusOK, adminv1.DeviceCredentials{
		Device:      deviceDTO(device, current.DeviceID),
		DeviceToken: token,
		ServerID:    info.ServerID,
	})
	return nil
}
