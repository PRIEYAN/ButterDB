package shell

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
)

func pickerFixture(t *testing.T) (string, *filePicker, *[]string, fyne.Window) {
	t.Helper()
	test.NewTempApp(t)
	dir := t.TempDir()
	for _, name := range []string{"b.mp3", "a.mp3", "c.txt", ".hidden"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	pickerLastDir = dir
	t.Cleanup(func() { pickerLastDir = "" })
	win := test.NewTempWindow(t, widget.NewLabel("app"))
	win.Resize(fyne.NewSize(1400, 1000))
	var got []string
	ShowFilesOpen(win, "Upload files", func(paths []string) { got = paths })
	frame := win.Canvas().Overlays().Top().(*widget.PopUp).Content.(*fileDialogFrame)
	return dir, frame.content.(*filePicker), &got, win
}

func names(p *filePicker) []string {
	var out []string
	for _, e := range p.entries {
		out = append(out, e.name)
	}
	return out
}

func index(t *testing.T, p *filePicker, name string) int {
	for i, e := range p.entries {
		if e.name == name {
			return i
		}
	}
	t.Fatalf("%s not listed", name)
	return -1
}

func TestFilePickerListsFoldersFirstAndHidesDotFiles(t *testing.T) {
	_, p, _, _ := pickerFixture(t)
	want := []string{"(Parent)", "sub", "a.mp3", "b.mp3", "c.txt"}
	if got := names(p); !reflect.DeepEqual(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
}

func TestFilePickerCtrlClickSelectsSeveralAndUploadsAll(t *testing.T) {
	dir, p, got, win := pickerFixture(t)
	p.click(index(t, p, "a.mp3"), 0)
	p.click(index(t, p, "c.txt"), fyne.KeyModifierControl)
	if p.open.Text != "Open 2" || p.status.Text != "2 files selected" {
		t.Fatalf("footer = %q / %q", p.open.Text, p.status.Text)
	}
	p.click(index(t, p, "a.mp3"), fyne.KeyModifierControl)
	p.click(index(t, p, "b.mp3"), fyne.KeyModifierControl)
	p.confirm()
	want := []string{filepath.Join(dir, "b.mp3"), filepath.Join(dir, "c.txt")}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("chosen = %v, want %v", *got, want)
	}
	if win.Canvas().Overlays().Top() != nil {
		t.Fatal("picker still open after Open")
	}
}

func TestFilePickerPlainClickReplacesSelection(t *testing.T) {
	_, p, _, _ := pickerFixture(t)
	p.click(index(t, p, "a.mp3"), 0)
	p.click(index(t, p, "b.mp3"), 0)
	if len(p.chosen()) != 1 || p.open.Text != "Open" {
		t.Fatalf("chosen = %v", p.chosen())
	}
}

func TestFilePickerCtrlASelectsEveryFileButNoFolders(t *testing.T) {
	dir, p, got, _ := pickerFixture(t)
	p.TypedShortcut(&fyne.ShortcutSelectAll{})
	p.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	want := []string{filepath.Join(dir, "a.mp3"), filepath.Join(dir, "b.mp3"), filepath.Join(dir, "c.txt")}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("chosen = %v, want %v", *got, want)
	}
}

func TestFilePickerFolderClickNavigatesAndEscapeCancels(t *testing.T) {
	dir, p, got, win := pickerFixture(t)
	p.click(index(t, p, "sub"), 0)
	if p.dir != filepath.Join(dir, "sub") || len(p.chosen()) != 0 {
		t.Fatalf("dir = %s", p.dir)
	}
	p.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
	if *got != nil || win.Canvas().Overlays().Top() != nil {
		t.Fatal("escape should close without choosing")
	}
}

func TestFilePickerIsLargerThanFyneDialog(t *testing.T) {
	if got := pickerSize(fyne.NewSize(1920, 1080)); got.Width < 1600 || got.Height < 900 {
		t.Fatalf("size = %v", got)
	}
	if got := pickerSize(fyne.NewSize(800, 500)); got != fyne.NewSize(800, 500) {
		t.Fatalf("small window size = %v, want clamped to window", got)
	}
}
