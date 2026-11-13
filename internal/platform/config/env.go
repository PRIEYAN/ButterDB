package config

import (
	"encoding"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

const (
	configPathVariable = "BUTTERDB_CONFIG"
)

var reservedEnvPrefixes = []string{"BUTTERDB_TGTEST", "BUTTERDB_S3COMPAT_"}

func isReservedEnv(name string) bool {
	if name == configPathVariable {
		return true
	}
	for _, prefix := range reservedEnvPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func parseEnviron(environ []string) map[string]string {
	values := make(map[string]string, len(environ))
	for _, entry := range environ {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		values[name] = value
	}
	return values
}

func (l *loader) applyEnvironment(values map[string]string) {
	byEnv := make(map[string]field)
	for _, f := range walkFields(&l.cfg) {
		byEnv[f.env] = f
	}
	for _, name := range sortedNames(values) {
		if !strings.HasPrefix(name, envPrefix) || isReservedEnv(name) {
			continue
		}
		f, known := byEnv[name]
		if !known {
			l.addError(name, "unknown environment variable")
			continue
		}
		if err := setFromText(f, values[name]); err != nil {
			l.addError(f.key, fmt.Sprintf("environment variable %s: %s", name, describeSetError(f, err)))
		}
	}
}

func describeSetError(f field, err error) string {
	if f.kind == kindPath {
		return "invalid value"
	}
	return err.Error()
}

func setFromText(f field, text string) error {
	target := f.value
	if unmarshaler, ok := target.Addr().Interface().(encoding.TextUnmarshaler); ok {
		return unmarshaler.UnmarshalText([]byte(text))
	}
	switch target.Kind() {
	case reflect.String:
		target.SetString(text)
	case reflect.Bool:
		return setBool(target, text)
	case reflect.Int:
		return setInt(target, text)
	case reflect.Slice:
		return setList(target, text)
	default:
		return fmt.Errorf("unsupported type %s", target.Type())
	}
	return nil
}

func setBool(target reflect.Value, text string) error {
	switch text {
	case "true":
		target.SetBool(true)
	case "false":
		target.SetBool(false)
	default:
		return fmt.Errorf("must be true or false")
	}
	return nil
}

func setInt(target reflect.Value, text string) error {
	number, err := strconv.Atoi(text)
	if err != nil {
		return fmt.Errorf("must be an integer")
	}
	target.SetInt(int64(number))
	return nil
}

func setList(target reflect.Value, text string) error {
	items := splitList(text)
	if target.Type().Elem().Kind() == reflect.String {
		target.Set(reflect.ValueOf(items))
		return nil
	}
	numbers := make([]int, 0, len(items))
	for _, item := range items {
		number, err := strconv.Atoi(item)
		if err != nil {
			return fmt.Errorf("list items must be integers")
		}
		numbers = append(numbers, number)
	}
	target.Set(reflect.ValueOf(numbers))
	return nil
}

func splitList(text string) []string {
	items := []string{}
	if text == "" {
		return items
	}
	for _, item := range strings.Split(text, ",") {
		items = append(items, strings.TrimSpace(item))
	}
	return items
}
