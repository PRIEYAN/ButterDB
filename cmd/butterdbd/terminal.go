package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

type consoleTerminal struct {
	reader *bufio.Reader
	out    io.Writer
	fd     int
}

func newConsoleTerminal(in io.Reader, out io.Writer) *consoleTerminal {
	fd := -1
	if file, ok := in.(*os.File); ok {
		fd = int(file.Fd())
	}
	return &consoleTerminal{reader: bufio.NewReader(in), out: out, fd: fd}
}

func (t *consoleTerminal) Prompt(label string) (string, error) {
	fmt.Fprintf(t.out, "%s: ", label)
	return t.readLine()
}

func (t *consoleTerminal) PromptSecret(label string) (string, error) {
	fmt.Fprintf(t.out, "%s: ", label)
	restore, hidden := hideEcho(t.fd)
	defer restore()
	line, err := t.readLine()
	if hidden {
		fmt.Fprintln(t.out)
	}
	return line, err
}

func (t *consoleTerminal) Say(text string) {
	fmt.Fprintln(t.out, text)
}

func (t *consoleTerminal) readLine() (string, error) {
	line, err := t.reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
