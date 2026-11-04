package reviewer

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/icaruszezen/trainmaster/backend/internal/review"
)

// jsonFields returns the JSON field names of a struct type with the type
// of each (embedded structs flattened).
func jsonFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			for k, v := range jsonFields(f.Type) {
				out[k] = v
			}
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}

// shape describes a JSON value's shape, so that types of two packages
// can be compared ("struct", "[]struct", "*int", ...).
func shape(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + shape(t.Elem())
	case reflect.Slice:
		return "[]" + shape(t.Elem())
	case reflect.Struct:
		if t.PkgPath() == "time" {
			return "time"
		}
		return "struct"
	}
	return t.Kind().String()
}

// TestJSONMatchesPlatform checks that the review editor gets from the local
// reviewer what it gets from the platform: the same fields with the same
// shapes (the local types mirror internal/review's, which the program does
// not import: it would pull in the database).
func TestJSONMatchesPlatform(t *testing.T) {
	for _, tt := range []struct {
		local, platform any
		subset          bool // the local type has some of the platform's fields
	}{
		{Image{}, review.Image{}, false},
		{ImageDetail{}, review.ImageDetail{}, false},
		{Neighbors{}, review.Neighbors{}, false},
		{Counts{}, review.Counts{}, false},
		{Dataset{}, review.Dataset{}, false},
		{Label{}, review.Label{}, false},
		{ClassList{}, review.ClassList{}, false},
		{Summary{}, review.Review{}, true},
	} {
		lt, pt := reflect.TypeOf(tt.local), reflect.TypeOf(tt.platform)
		local, platform := jsonFields(lt), jsonFields(pt)
		for name, typ := range local {
			ptyp, ok := platform[name]
			switch {
			case !ok:
				t.Errorf("%s.%s is not in %s", lt.Name(), name, pt)
			case shape(typ) != shape(ptyp):
				t.Errorf("%s.%s is %s, the platform's %s", lt.Name(), name, shape(typ), shape(ptyp))
			}
		}
		if !tt.subset {
			for name := range platform {
				if _, ok := local[name]; !ok {
					t.Errorf("%s has no field %s of %s", lt.Name(), name, pt)
				}
			}
		}
	}
	// The editor's ReviewSummary (frontend) reads these fields of a review.
	summary := jsonFields(reflect.TypeOf(Summary{}))
	for _, name := range []string{"id", "name", "description", "model", "options", "status", "counts", "datasets", "project"} {
		if _, ok := summary[name]; !ok {
			t.Errorf("Summary has no %s", name)
		}
	}
	// The filters and page sizes are the platform's.
	local := []any{filterAll, filterReviewed, filterEdited, filterUnedited, filterWith, filterWithout, filterFailed,
		defaultImageLimit, maxImageLimit}
	platform := []any{review.FilterAll, review.FilterReviewed, review.FilterEdited, review.FilterUnedited, review.FilterWith,
		review.FilterWithout, review.FilterFailed, review.DefaultImageLimit, review.MaxImageLimit}
	if !slices.Equal(local, platform) {
		t.Errorf("filters %v, the platform's %v", local, platform)
	}
}
