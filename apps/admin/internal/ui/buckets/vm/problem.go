package vm

import (
	"net/http"
	"time"

	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

func localProblem(code adminv1.Code, details map[string]any, now time.Time) *model.Problem {
	p := model.Describe(&adminv1.APIError{
		Status: http.StatusBadRequest,
		Body:   adminv1.ErrorBody{Code: code, Details: details},
	}, now)
	return &p
}
