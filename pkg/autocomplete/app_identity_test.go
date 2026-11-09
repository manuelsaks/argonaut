package autocomplete

import (
	"reflect"
	"testing"

	"github.com/darksworm/argonaut/pkg/model"
)

func TestApplicationSuggestionsQualifyDuplicateNames(t *testing.T) {
	a, b := "team-a", "team-b"
	state := &model.AppState{Apps: []model.App{{Name: "shared", AppNamespace: &a}, {Name: "shared", AppNamespace: &b}, {Name: "unique", AppNamespace: &a}}}
	engine := NewAutocompleteEngine()
	if got := engine.getAppSuggestions("", state); !reflect.DeepEqual(got, []string{"team-a/shared", "team-b/shared", "unique"}) {
		t.Fatalf("ambiguous suggestions: %v", got)
	}
	if got := engine.getAppSuggestions("team-b/sh", state); !reflect.DeepEqual(got, []string{"team-b/shared"}) {
		t.Fatalf("qualified completion failed: %v", got)
	}
	if got := engine.getAppSuggestions("team-a/un", state); !reflect.DeepEqual(got, []string{"team-a/unique"}) {
		t.Fatalf("qualified unique completion failed: %v", got)
	}
}
