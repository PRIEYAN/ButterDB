package jobrepo

import "fmt"

var allowedUploadTransitions = map[UploadState][]UploadState{
	UploadPrepared:      {UploadBodySending, UploadPutAttempted, UploadAborted},
	UploadBodySending:   {UploadBodySent, UploadUncertainBody, UploadAborted},
	UploadUncertainBody: {UploadBodySent, UploadAborted},
	UploadBodySent:      {UploadPutAttempted, UploadAborted},
	UploadPutAttempted:  {UploadCommitted, UploadUncertainPut},
	UploadUncertainPut:  {UploadCommitted, UploadPutAttempted, UploadAborted},
	UploadCommitted:     nil,
	UploadAborted:       nil,
}

var allowedDeleteTransitions = map[DeleteState][]DeleteState{
	DeletePrepared:     {DeleteDelAttempted, DeleteAborted},
	DeleteDelAttempted: {DeleteCommitted, DeleteUncertainDel},
	DeleteUncertainDel: {DeleteCommitted, DeleteDelAttempted, DeleteAborted},
	DeleteCommitted:    nil,
	DeleteAborted:      nil,
}

func ValidateUploadTransition(from, to UploadState) error {
	targets, known := allowedUploadTransitions[from]
	if !known {
		return fmt.Errorf("jobrepo: upload state %q: %w", from, ErrInvalidTransition)
	}
	for _, target := range targets {
		if target == to {
			return nil
		}
	}
	return fmt.Errorf("jobrepo: upload %q to %q: %w", from, to, ErrInvalidTransition)
}

func ValidateDeleteTransition(from, to DeleteState) error {
	targets, known := allowedDeleteTransitions[from]
	if !known {
		return fmt.Errorf("jobrepo: delete state %q: %w", from, ErrInvalidTransition)
	}
	for _, target := range targets {
		if target == to {
			return nil
		}
	}
	return fmt.Errorf("jobrepo: delete %q to %q: %w", from, to, ErrInvalidTransition)
}

func (s UploadState) Terminal() bool {
	return s == UploadCommitted || s == UploadAborted
}

func (s DeleteState) Terminal() bool {
	return s == DeleteCommitted || s == DeleteAborted
}

func validUploadState(state UploadState) bool {
	_, known := allowedUploadTransitions[state]
	return known
}

func validDeleteState(state DeleteState) bool {
	_, known := allowedDeleteTransitions[state]
	return known
}
