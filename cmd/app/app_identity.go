package main

import (
	"fmt"
	"sort"

	"github.com/darksworm/argonaut/pkg/model"
)

// selectedApplications resolves the entire selection before starting any API
// calls. Legacy unqualified names are accepted only when they are unambiguous.
// The returned values are snapshots; async commands must not re-read UI state.
func (m *Model) selectedApplications() ([]model.App, error) {
	var keys []string
	for key, selected := range m.state.Selections.SelectedApps {
		if selected {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	apps := make([]model.App, 0, len(keys))
	for _, key := range keys {
		app := m.findAppByNameAndNamespace(key, "")
		if app == nil {
			return nil, fmt.Errorf("application %q is missing or ambiguous; select it again using namespace/name", key)
		}
		snapshot := *app
		if app.AppNamespace != nil {
			namespace := *app.AppNamespace
			snapshot.AppNamespace = &namespace
		}
		apps = append(apps, snapshot)
	}
	return apps, nil
}

// selectedTreeApplication preserves the owning application in multi-app trees.
func (m *Model) selectedTreeApplication() (string, *string) {
	if m.treeView == nil {
		return "", nil
	}
	key := m.treeView.SelectedNodeApp()
	name, ns := model.SplitAppKey(key)
	if ns != "" {
		return name, &ns
	}
	if app := m.state.UI.TreeApp; app != nil && app.Name == name {
		return name, app.AppNamespace
	}
	if app := m.findAppByNameAndNamespace(name, ""); app != nil {
		return app.Name, app.AppNamespace
	}
	for _, app := range m.state.Apps {
		if app.Name == name {
			return "", nil // Ambiguous owner: fail closed.
		}
	}
	return name, nil
}

func (m *Model) operationApplication(name string, namespace *string) (model.App, error) {
	parsedName, parsedNamespace := model.SplitAppKey(name)
	if parsedNamespace != "" {
		if namespace != nil && *namespace != "" && *namespace != parsedNamespace {
			return model.App{}, fmt.Errorf("conflicting namespaces for application %q", name)
		}
		return model.App{Name: parsedName, AppNamespace: &parsedNamespace}, nil
	}
	if namespace != nil && *namespace != "" {
		ns := *namespace
		return model.App{Name: name, AppNamespace: &ns}, nil
	}
	if app := m.findAppByNameAndNamespace(name, ""); app != nil {
		snapshot := *app
		if app.AppNamespace != nil {
			ns := *app.AppNamespace
			snapshot.AppNamespace = &ns
		}
		return snapshot, nil
	}
	for _, app := range m.state.Apps {
		if app.Name == name {
			return model.App{}, fmt.Errorf("application %q is ambiguous; select namespace/name", name)
		}
	}
	return model.App{Name: name}, nil // Legacy default-namespace application.
}
