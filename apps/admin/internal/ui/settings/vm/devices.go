package vm

import (
	"context"
	"strings"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

type DeviceRow struct {
	Device  adminv1.Device
	This    bool
	Revoked bool
}

type DeviceState struct {
	Found   bool
	Current adminv1.Device
}

type DevicesState struct {
	Loaded bool
	Rows   []DeviceRow
}

const NoticeTokenRotated = "Device token rotated."

type RevokeOutcome int

const (
	RevokeFailed RevokeOutcome = iota
	RevokeDone
	RevokeNeedsLastConfirmation
	RevokedThisDevice
)

func (o RevokeOutcome) Route() model.Route {
	if o == RevokedThisDevice {
		return model.RouteConnect
	}
	return ""
}

func (v *ViewModel) RefreshDevices(ctx context.Context) {
	v.begin()
	list, err := v.client.ListDevices(ctx)
	if err != nil {
		v.finish(func(*State) {}, v.problemOf(err))
		return
	}
	v.finish(func(s *State) { v.applyDevices(s, list) }, nil)
}

func (v *ViewModel) applyDevices(s *State, list adminv1.DeviceList) {
	own := v.snapshot().Profile.DeviceID
	rows := make([]DeviceRow, 0, len(list.Devices))
	s.Device = DeviceState{}
	for _, d := range list.Devices {
		this := d.Current || (own != "" && d.DeviceID == own && !anyCurrent(list.Devices))
		rows = append(rows, DeviceRow{Device: d, This: this, Revoked: d.RevokedAt != nil})
		if this {
			s.Device = DeviceState{Found: true, Current: d}
		}
	}
	s.Devices = DevicesState{Loaded: true, Rows: rows}
}

func anyCurrent(devices []adminv1.Device) bool {
	for _, d := range devices {
		if d.Current {
			return true
		}
	}
	return false
}

func (v *ViewModel) ownDeviceID() string {
	if cur := v.state.Get().Device; cur.Found {
		return cur.Current.DeviceID
	}
	return v.snapshot().Profile.DeviceID
}

func (v *ViewModel) RenameDevice(ctx context.Context, name string) {
	v.begin()
	name = strings.TrimSpace(name)
	if n := len([]rune(name)); n < 1 || n > maxNameLength {
		v.fail(inlineProblem(fieldName, textNameLength))
		return
	}
	id := v.ownDeviceID()
	renamed, err := v.client.RenameDevice(ctx, id, adminv1.UpdateDeviceRequest{Name: name})
	if err != nil {
		v.finish(func(*State) {}, v.problemOf(err))
		return
	}
	profileErr := v.saveProfileDeviceName(renamed.Name, name)
	v.finish(func(s *State) {
		v.replaceDevice(s, renamed, id, name)
		s.Connection = connectionFor(v.snapshot(), s.Connection)
	}, v.problemOf(profileErr))
}

func (v *ViewModel) saveProfileDeviceName(serverName, requested string) error {
	s := v.snapshot()
	name := serverName
	if name == "" {
		name = requested
	}
	updated := s.Profile
	updated.DeviceName = name
	if s.Profiles != nil {
		if err := s.Profiles.Put(updated); err != nil {
			return err
		}
	}
	v.setProfile(updated)
	return nil
}

func (v *ViewModel) replaceDevice(s *State, renamed adminv1.Device, id, requested string) {
	for i := range s.Devices.Rows {
		row := &s.Devices.Rows[i]
		if row.Device.DeviceID != id {
			continue
		}
		updated := renamed
		if updated.DeviceID == "" {
			updated = row.Device
			updated.Name = requested
		}
		row.Device = updated
		if row.This {
			s.Device = DeviceState{Found: true, Current: updated}
		}
	}
}

func (v *ViewModel) RotateToken(ctx context.Context) bool {
	v.begin()
	s := v.snapshot()
	if s.Tokens == nil {
		v.fail(dialogProblem(textNotAvailable))
		return false
	}
	creds, err := v.client.RotateDeviceToken(ctx)
	if err != nil {
		v.finish(func(*State) {}, v.problemOf(err))
		return false
	}
	if err := s.Tokens.Set(s.Profile.ServerID, creds.DeviceToken); err != nil {
		v.fail(bannerProblem(textRotatedNotSaved))
		return false
	}
	if s.SetClientToken != nil {
		s.SetClientToken(creds.DeviceToken)
	}
	issued := creds.Device.TokenIssuedAt
	if issued.IsZero() {
		issued = v.now()
	}
	updated := s.Profile
	updated.TokenIssuedAt = issued
	var profileErr error
	if s.Profiles != nil {
		profileErr = s.Profiles.Put(updated)
	}
	v.setProfile(updated)
	v.finish(func(st *State) {
		st.Connection = connectionFor(v.snapshot(), st.Connection)
		if st.Device.Found {
			st.Device.Current.TokenIssuedAt = issued
		}
		st.Notice = NoticeTokenRotated
	}, v.problemOf(profileErr))
	return true
}

func (v *ViewModel) Revoke(ctx context.Context, deviceID string, confirmLast bool) RevokeOutcome {
	v.begin()
	err := v.client.RevokeDevice(ctx, deviceID, confirmLast)
	if adminv1.IsCode(err, adminv1.CodeLastDevice) && !confirmLast {
		v.finish(func(*State) {}, nil)
		return RevokeNeedsLastConfirmation
	}
	if err != nil {
		return v.revokeFailed(ctx, err)
	}
	if deviceID == v.ownDeviceID() {
		v.forgetThisServer()
		v.finish(func(*State) {}, nil)
		return RevokedThisDevice
	}
	v.RefreshDevices(ctx)
	return RevokeDone
}

func (v *ViewModel) revokeFailed(ctx context.Context, err error) RevokeOutcome {
	problem := v.problemOf(err)
	if problem != nil && problem.Code == adminv1.CodeDeviceNotFound {
		v.RefreshDevices(ctx)
	}
	v.finish(func(*State) {}, problem)
	return RevokeFailed
}

func (v *ViewModel) forgetThisServer() {
	s := v.snapshot()
	if s.Tokens != nil {
		_ = s.Tokens.Delete(s.Profile.ServerID)
	}
	if s.Profiles != nil {
		_ = s.Profiles.Delete(s.Profile.ServerID)
	}
}
