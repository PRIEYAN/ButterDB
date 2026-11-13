package access

import "fmt"

type Action string

const (
	ActionRead   Action = "read"
	ActionList   Action = "list"
	ActionWrite  Action = "write"
	ActionDelete Action = "delete"
)

func AllActions() []Action {
	return []Action{ActionRead, ActionList, ActionWrite, ActionDelete}
}

func ParseAction(raw string) (Action, error) {
	action := Action(raw)
	switch action {
	case ActionRead, ActionList, ActionWrite, ActionDelete:
		return action, nil
	}
	return "", fmt.Errorf("access: action: %w", ErrInvalidAction)
}

func (action Action) String() string {
	return string(action)
}
