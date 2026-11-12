package mapping

import (
	"reflect"
	"testing"
)

func TestParseTagFieldStringSlice(t *testing.T) {
	got := parseTagField([]string{
		"equestria",
		"meta:equestria at war mod",
		"",
		"wings",
	})

	want := []string{
		"equestria",
		"meta:equestria at war mod",
		"wings",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf(
			"parseTagField() = %#v, want %#v",
			got,
			want,
		)
	}
}
