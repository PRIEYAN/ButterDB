package handlers

import (
	"net/http"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
)

const replicaPhase = 12

func (a *api) registerReplicaRoutes() {
	a.authed("GET "+adminv1.PathReplica, a.replicaNotImplemented)
	a.authed("POST "+adminv1.PathReplicaTelegramPhone, a.replicaNotImplemented)
	a.authed("POST "+adminv1.PathReplicaTelegramCode, a.replicaNotImplemented)
	a.authed("POST "+adminv1.PathReplicaTelegramPass, a.replicaNotImplemented)
	a.authed("POST "+adminv1.PathReplicaTelegramCancel, a.replicaNotImplemented)
	a.authed("POST "+adminv1.PathReplicaUnlink, a.replicaNotImplemented)
}

func (a *api) replicaNotImplemented(w http.ResponseWriter, r *http.Request) error {
	return failure(adminv1.CodeNotImplemented, map[string]any{"phase": replicaPhase})
}
