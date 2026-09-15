package stremio

import (
	"reflect"
	"sort"
	"text/template"
)

// The machine-readable account of what a result-format template can read.
//
// Both halves are derived rather than listed: the fields by reflecting over
// FormatContext, the helpers from the FuncMap the templates are parsed with.
// A hand-written list would be a second copy of both, and a copy drifts the
// first time a field is added without someone remembering it exists.

// FormatField is one value a template may read, as the path a template writes
// it at — leading dot included, so ".Probed.HDR" is copy-pasteable into
// {{ }}.
type FormatField struct {
	Path string `json:"path"`
	// Type is how a template sees the value: "string", "num", "bool",
	// "list<string>", "list<num>", or "list" for a list of records whose own
	// fields are reported under a "[]" path.
	Type string `json:"type"`
}

// FormatHelper is one function a template may call.
type FormatHelper struct {
	Name string `json:"name"`
	// Args is how many arguments it takes, or -1 when it is variadic.
	Args int `json:"args"`
}

// FormatFields lists every readable path on FormatContext, sorted. Nested
// records are flattened to dotted paths, and a list of records contributes
// both the list itself and its element's fields under a "[]" segment:
// ".MatchedRules" and ".MatchedRules[].Name".
func FormatFields() []FormatField {
	out := formatFieldsOf(reflect.TypeOf(FormatContext{}), "")
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func formatFieldsOf(t reflect.Type, prefix string) []FormatField {
	var out []FormatField
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported: a template cannot reach it
		}
		path := prefix + "." + f.Name
		switch kind := formatFieldType(f.Type); kind {
		case "record":
			out = append(out, formatFieldsOf(f.Type, path)...)
		case "list":
			out = append(out, FormatField{Path: path, Type: "list"})
			out = append(out, formatFieldsOf(f.Type.Elem(), path+"[]")...)
		default:
			out = append(out, FormatField{Path: path, Type: kind})
		}
	}
	return out
}

// formatFieldType maps a Go type to what a template author sees, or "record"
// and "list" for the two shapes that need recursing into.
func formatFieldType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "num"
	case reflect.Struct:
		return "record"
	case reflect.Ptr:
		return formatFieldType(t.Elem())
	case reflect.Slice, reflect.Array:
		switch elem := formatFieldType(t.Elem()); elem {
		case "record":
			return "list"
		default:
			return "list<" + elem + ">"
		}
	}
	return t.Kind().String()
}

// FormatHelpers lists the template functions, sorted. They come from the same
// FuncMap every template is parsed with, so the report cannot advertise a
// helper a template would fail to parse.
func FormatHelpers() []FormatHelper {
	out := helpersOf(formatTemplateFuncs)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func helpersOf(funcs template.FuncMap) []FormatHelper {
	out := make([]FormatHelper, 0, len(funcs))
	for name, fn := range funcs {
		h := FormatHelper{Name: name, Args: -1}
		if t := reflect.TypeOf(fn); t != nil && t.Kind() == reflect.Func && !t.IsVariadic() {
			h.Args = t.NumIn()
		}
		out = append(out, h)
	}
	return out
}

// FormatSyntaxNote is the one thing about the templates that neither list can
// convey: they are Go text/template, so the control flow a client may emit is
// that package's and not something StreamNZB defines.
const FormatSyntaxNote = "Go text/template: {{.Field}}, {{if}}, {{range}}, {{with}} and | pipelines"
