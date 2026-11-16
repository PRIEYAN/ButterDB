package connect

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func labelTextPresent(root fyne.CanvasObject, text string) bool {
	for _, o := range test.LaidOutObjects(root) {
		if l, ok := o.(*widget.Label); ok && l.Text == text {
			return true
		}
	}
	return false
}

func findButtonByText(root fyne.CanvasObject, text string) *widget.Button {
	for _, o := range test.LaidOutObjects(root) {
		switch b := o.(type) {
		case *widget.Button:
			if b.Text == text {
				return b
			}
		case *actionButton:
			if b.Text == text {
				return &b.Button
			}
		}
	}
	return nil
}

func buttonPresent(root fyne.CanvasObject, text string) bool {
	return findButtonByText(root, text) != nil
}
