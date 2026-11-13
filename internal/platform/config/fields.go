package config

import (
	"reflect"
	"strings"
)

type fieldKind int

const (
	kindString fieldKind = iota
	kindPath
	kindAddress
)

type field struct {
	key   string
	env   string
	kind  fieldKind
	tag   reflect.StructTag
	value reflect.Value
}

const envPrefix = "BUTTERDB_"

func walkFields(cfg *Config) []field {
	var fields []field
	collectFields(reflect.ValueOf(cfg).Elem(), "", &fields)
	return fields
}

func collectFields(section reflect.Value, prefix string, out *[]field) {
	sectionType := section.Type()
	for i := 0; i < section.NumField(); i++ {
		structField := sectionType.Field(i)
		key := prefix + structField.Tag.Get("toml")
		value := section.Field(i)
		if value.Kind() == reflect.Struct {
			collectFields(value, key+".", out)
			continue
		}
		*out = append(*out, field{
			key:   key,
			env:   envName(key),
			kind:  kindOf(structField.Tag),
			tag:   structField.Tag,
			value: value,
		})
	}
}

func kindOf(tag reflect.StructTag) fieldKind {
	switch tag.Get("kind") {
	case "path":
		return kindPath
	case "address":
		return kindAddress
	}
	return kindString
}

func envName(key string) string {
	return envPrefix + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}
