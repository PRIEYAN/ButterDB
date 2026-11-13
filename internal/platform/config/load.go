package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

var ErrInvalid = errors.New("invalid configuration")

type loader struct {
	cfg      Config
	findings []Finding
}

func Load(opts LoadOptions) (Config, []Finding, error) {
	l := &loader{cfg: tokenDefaults()}
	environment := parseEnviron(opts.Environ)
	l.applyFile(opts)
	l.applyEnvironment(environment)
	l.resolvePaths(environment)
	l.validate()
	return l.cfg, l.findings, l.failure()
}

func (l *loader) failure() error {
	count := 0
	for _, f := range l.findings {
		if f.Severity == SeverityError {
			count++
		}
	}
	if count == 0 {
		return nil
	}
	return fmt.Errorf("config: %w: %d error(s)", ErrInvalid, count)
}

func (l *loader) addError(key, message string) {
	l.findings = append(l.findings, Finding{Severity: SeverityError, Key: key, Message: message})
}

func (l *loader) applyFile(opts LoadOptions) {
	if opts.Path == "" {
		return
	}
	data, err := os.ReadFile(opts.Path)
	if errors.Is(err, os.ErrNotExist) && !opts.PathExplicit {
		return
	}
	if err != nil {
		l.addError("config", "cannot read configuration file: "+describeReadError(err))
		return
	}
	l.decodeFile(data)
}

func describeReadError(err error) string {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}

func (l *loader) decodeFile(data []byte) {
	meta, err := toml.Decode(string(data), &l.cfg)
	if err != nil {
		l.addError(decodeErrorKey(err), "invalid configuration file: "+decodeErrorMessage(err))
		return
	}
	for _, key := range meta.Undecoded() {
		l.addError(key.String(), "unknown key")
	}
}

func decodeErrorKey(err error) string {
	var parseErr toml.ParseError
	if errors.As(err, &parseErr) && parseErr.LastKey != "" {
		return parseErr.LastKey
	}
	return lastKeyInMessage(err.Error())
}

func lastKeyInMessage(message string) string {
	const marker = `(last key "`
	_, rest, found := strings.Cut(message, marker)
	if !found {
		return "config"
	}
	key, _, closed := strings.Cut(rest, `")`)
	if !closed {
		return "config"
	}
	return key
}

func decodeErrorMessage(err error) string {
	var parseErr toml.ParseError
	if errors.As(err, &parseErr) {
		return fmt.Sprintf("line %d: %s", parseErr.Position.Line, parseErr.Message)
	}
	return err.Error()
}

func (l *loader) resolvePaths(environment map[string]string) {
	defaults := tokenDefaults()
	credentialsDir := environment[credentialsDirName]
	for _, f := range walkFields(&l.cfg) {
		if f.kind != kindPath {
			continue
		}
		value := f.value.String()
		if !strings.HasPrefix(value, credentialsDirRef) {
			continue
		}
		if credentialsDir == "" {
			l.addError(f.key, "references "+credentialsDirRef+" but "+credentialsDirName+" is not set")
			continue
		}
		f.value.SetString(credentialsDir + strings.TrimPrefix(value, credentialsDirRef))
	}
	expandDefaultTokens(&l.cfg, &defaults, l.cfg.DataDir)
}

func sortedNames(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
