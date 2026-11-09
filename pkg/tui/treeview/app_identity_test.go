package treeview

import (
	"testing"

	"github.com/darksworm/argonaut/pkg/api"
)

func TestSameNamedApplicationsKeepResourceSelectionsSeparate(t *testing.T) {
	v := NewTreeView(100, 30)
	for _, key := range []string{"team-a/shared", "team-b/shared"} {
		v.UpsertAppTree(key, &api.ResourceTree{Nodes: []api.ResourceNode{{UID: "same-uid", Kind: "ConfigMap", Name: "cfg"}}})
	}
	if v.VisibleCount() != 4 {
		t.Fatalf("applications or resources collided: %d rows", v.VisibleCount())
	}
	for _, index := range []int{1, 3} {
		v.SetSelectedIndex(index)
		if !v.ToggleSelection() {
			t.Fatal("could not select resource")
		}
		detail, ok := v.SelectedResourceDetail()
		if !ok || detail.AppName != "shared" || detail.AppNamespace == nil || detail.UID != "same-uid" {
			t.Fatalf("lost resource identity: %#v", detail)
		}
	}
	selected := v.GetSelectedResources()
	if len(selected) != 2 {
		t.Fatalf("expected two selections, got %#v", selected)
	}
	seen := map[string]bool{}
	for _, resource := range selected {
		if resource.AppName != "shared" || resource.AppNamespace == nil {
			t.Fatalf("lost owning application: %#v", resource)
		}
		seen[*resource.AppNamespace] = true
	}
	if !seen["team-a"] || !seen["team-b"] {
		t.Fatalf("wrong application namespaces: %v", seen)
	}
}
