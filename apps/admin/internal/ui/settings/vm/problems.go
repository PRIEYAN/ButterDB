package vm

import (
	"github.com/PRIEYAN/ButterDB/api/adminv1"
	"github.com/PRIEYAN/ButterDB/apps/admin/internal/model"
)

const (
	fieldConcurrency = "upload_concurrency"
	fieldClipboard   = "clipboard_clear_seconds"
	fieldTheme       = "theme"
	fieldDownloadDir = "download_dir"
)

const (
	textNotAvailable     = "This action is not available here."
	textNoProject        = "No project is open."
	textEnterOldPassword = "Enter the current project password."
	textConcurrency      = "Check this value: upload concurrency is 1 to 4."
	textClipboardSeconds = "Check this value: seconds must be 0 or more."
	textThemeUnknown     = "Check this value: choose system, light or dark."
	textDownloadDirEmpty = "Check this value: choose a folder."
)

func inlineProblem(field, title string) model.Problem {
	return model.Problem{
		Code:         adminv1.CodeValidationFailed,
		Title:        title,
		Field:        field,
		Presentation: model.PresentInline,
	}
}

func dialogProblem(title string) model.Problem {
	return model.Problem{Title: title, Presentation: model.PresentDialog}
}
