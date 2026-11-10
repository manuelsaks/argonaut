package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/darksworm/argonaut/pkg/api"
	appcontext "github.com/darksworm/argonaut/pkg/context"
	"github.com/darksworm/argonaut/pkg/model"
	"github.com/darksworm/argonaut/pkg/services"
)

type inspectionTarget struct {
	ContextName                                string
	AppName, AppNamespace                      string
	Group, Version, Kind, Namespace, Name, UID string
	Application                                bool
	ApplicationSet                             bool
}

type resourceInspection struct {
	Target   inspectionTarget
	Kind     string
	Sequence int
	Cancel   context.CancelFunc
}

type resourceInspectionMsg struct {
	Target                inspectionTarget
	SwitchEpoch, Sequence int
	Text                  string
	Error                 error
}

func (m *Model) inspectionTargetForSelection() (inspectionTarget, error) {
	target := inspectionTarget{}
	switch m.state.Navigation.View {
	case model.ViewApplicationSets:
		items := m.getVisibleItems()
		i := m.state.Navigation.SelectedIdx
		if i < 0 || i >= len(items) {
			return target, fmt.Errorf("select an ApplicationSet first")
		}
		key, ok := items[i].(string)
		if !ok {
			return target, fmt.Errorf("select an ApplicationSet first")
		}
		contextName, localKey := model.SplitContextKey(key)
		target.ContextName = contextName
		target.Name, target.Namespace = model.SplitAppKey(localKey)
		target.Group, target.Version, target.Kind = "argoproj.io", "v1alpha1", "ApplicationSet"
		target.ApplicationSet = true
	case model.ViewApps:
		items := m.getVisibleItems()
		i := m.state.Navigation.SelectedIdx
		if i < 0 || i >= len(items) {
			return target, fmt.Errorf("select an application first")
		}
		app, ok := items[i].(model.App)
		if !ok {
			return target, fmt.Errorf("select an application first")
		}
		target.AppName = app.Name
		target.ContextName = app.ContextName
		if app.AppNamespace != nil {
			target.AppNamespace = *app.AppNamespace
		}
		target.Application = true
	case model.ViewTree:
		if m.treeView == nil {
			return target, fmt.Errorf("select a resource first")
		}
		owner, namespace := m.selectedTreeApplication()
		if owner == "" {
			return target, fmt.Errorf("resource owner is missing or ambiguous")
		}
		target.AppName = owner
		if namespace != nil {
			target.AppNamespace = *namespace
		}
		if m.treeView.IsSelectedSyntheticRoot() {
			target.Application = true
		} else {
			resource, ok := m.treeView.SelectedResourceDetail()
			if !ok {
				return target, fmt.Errorf("select a resource first")
			}
			target.Group, target.Version = resource.Group, resource.Version
			target.Kind, target.Namespace, target.Name, target.UID = resource.Kind, resource.Namespace, resource.Name, resource.UID
		}
	default:
		return target, fmt.Errorf("open applications or a resource tree first")
	}
	if target.Application {
		target.Group, target.Version, target.Kind = "argoproj.io", "v1alpha1", "Application"
		target.Name, target.Namespace = target.AppName, target.AppNamespace
	}
	return target, nil
}

func (m *Model) handleInspectSelection(kind string) (tea.Model, tea.Cmd) {
	target, err := m.inspectionTargetForSelection()
	if err != nil {
		return m, m.showFooterNotice(err.Error())
	}
	return m, m.startResourceInspection(target, kind)
}

func (m *Model) cancelResourceInspection() {
	if st := m.inspection; st != nil && st.Cancel != nil {
		st.Cancel()
	}
	m.inspection = nil
}

func (m *Model) startResourceInspection(target inspectionTarget, kind string) tea.Cmd {
	epoch := m.switchEpoch
	m.cancelResourceInspection()
	m.inspectionSeq++
	seq := m.inspectionSeq
	ctx, cancel := context.WithCancel(context.Background())
	m.inspection = &resourceInspection{Target: target, Kind: kind, Sequence: seq, Cancel: cancel}
	server := m.state.Server
	if target.ContextName != "" && m.allContexts != nil {
		server = m.allContexts.Servers[target.ContextName]
	}
	title := fmt.Sprintf("%s: %s/%s", kind, target.Kind, target.Name)
	if target.ContextName != "" {
		title = target.ContextName + " / " + title
	}
	if target.Namespace != "" {
		title += " (" + target.Namespace + ")"
	}
	m.state.Diff = &model.DiffState{Title: title, Content: []string{"Loading " + kind + "..."}, Loading: true, InspectionKind: kind}
	m.state.Mode = model.ModeDiff
	return func() tea.Msg {
		defer cancel()
		msg := resourceInspectionMsg{Target: target, SwitchEpoch: epoch, Sequence: seq}
		if server == nil {
			msg.Error = fmt.Errorf("no Argo CD server configured")
			return msg
		}
		service := api.NewApplicationService(server)
		objectCtx, objectCancel := appcontext.WithAPITimeout(ctx)
		var document *api.ResourceDocument
		if target.ApplicationSet {
			var raw json.RawMessage
			raw, msg.Error = service.GetApplicationSetObject(objectCtx, target.Name, target.Namespace)
			document = &api.ResourceDocument{Manifest: raw}
		} else if target.Application {
			var raw json.RawMessage
			raw, msg.Error = service.GetApplicationObject(objectCtx, target.AppName, target.AppNamespace)
			document = &api.ResourceDocument{Manifest: raw}
		} else {
			document, msg.Error = service.GetResourceObject(objectCtx, api.ResourceQuery{
				AppName: target.AppName, AppNamespace: target.AppNamespace,
				Group: target.Group, Version: target.Version, Kind: target.Kind, Namespace: target.Namespace, Name: target.Name,
			})
		}
		objectCancel()
		if msg.Error != nil {
			return msg
		}
		object, err := decodeInspectionObject(document.Manifest, target.Application)
		if err != nil {
			msg.Error = err
			return msg
		}
		if target.ApplicationSet {
			if object["apiVersion"] == nil {
				object["apiVersion"] = "argoproj.io/v1alpha1"
			}
			if object["kind"] == nil {
				object["kind"] = "ApplicationSet"
			}
		}
		if kind == "YAML" {
			msg.Text, msg.Error = inspectionYAML(object)
			if document.Desired {
				msg.Text = "# Desired manifest: resource is not present in the cluster.\n" + msg.Text
			}
			return msg
		}
		if target.ApplicationSet {
			msg.Text = describeInspectionObject(object, false, nil, "Inspect generated Applications for resource events.")
			msg.Text += "\nDiff (live -> desired):\n  Inspect a generated Application for its live / desired resource diff.\n"
			return msg
		}
		params := api.ListEventsParams{AppName: target.AppName, AppNamespace: target.AppNamespace}
		metadata, _ := object["metadata"].(map[string]any)
		if !target.Application {
			params.ResourceUID, _ = metadata["uid"].(string)
			if params.ResourceUID == "" {
				params.ResourceUID = target.UID
			}
			params.ResourceName, params.ResourceNamespace = target.Name, target.Namespace
		}
		var events []model.ResourceEvent
		var diffText string
		var diffLoad sync.WaitGroup
		diffLoad.Go(func() {
			diffCtx, diffCancel := appcontext.WithMinAPITimeout(ctx, 45*time.Second)
			defer diffCancel()
			ns := target.AppNamespace
			diffs, err := services.NewArgoApiService(server).GetResourceDiffs(diffCtx, server, target.AppName, &ns)
			if err != nil {
				diffText = "  Unavailable through Argo CD: " + err.Error() + "\n"
			} else {
				diffText = inspectionDiff(target, diffs)
			}
		})
		notice := ""
		if document.Desired {
			notice = "Not available: resource is not present in the cluster."
		} else if !target.Application && params.ResourceUID == "" {
			notice = "Not available: Argo CD returned no resource UID."
		} else {
			// A separate budget keeps an events failure from hiding a valid object.
			eventCtx, eventCancel := appcontext.WithAPITimeout(ctx)
			var eventErr error
			events, eventErr = service.ListEvents(eventCtx, params)
			eventCancel()
			if eventErr != nil {
				notice = "Unavailable through Argo CD: " + eventErr.Error()
			}
		}
		msg.Text = describeInspectionObject(object, document.Desired, events, notice)
		diffLoad.Wait()
		msg.Text += "\nDiff (live -> desired):\n" + diffText
		return msg
	}
}

func (m *Model) handleResourceInspection(msg resourceInspectionMsg) (tea.Model, tea.Cmd) {
	if msg.SwitchEpoch != m.switchEpoch {
		return m, nil
	}
	st := m.inspection
	if st == nil || st.Sequence != msg.Sequence || st.Target != msg.Target || m.state.Diff == nil || m.state.Diff.InspectionKind != st.Kind || (m.state.Mode != model.ModeDiff && m.state.Mode != model.ModeSearch) {
		return m, nil
	}
	m.state.Diff.Loading = false
	if msg.Error != nil {
		m.state.Diff.Content = []string{"Unable to load " + st.Kind + ":", "", inspectionPlainText(msg.Error.Error()), "", "Esc: return"}
		if requiresRelogin(msg.Error) {
			contextName := st.Target.ContextName
			if contextName == "" {
				contextName = m.currentContextName
			}
			if cmd, handled := m.maybeAutoReloginForContext(contextName); handled {
				return m, cmd
			}
		}
		return m, nil
	}
	m.state.Diff.Content = strings.Split(strings.TrimSuffix(inspectionPlainText(msg.Text), "\n"), "\n")
	if st.Kind == "Describe" {
		for i, line := range m.state.Diff.Content {
			if line == "Diff (live -> desired):" {
				m.state.Diff.SideContent = m.state.Diff.Content[i+1:]
				m.state.Diff.Content = m.state.Diff.Content[:i]
				break
			}
		}
	}
	return m, nil
}

func inspectionPlainText(text string) string {
	text = ansi.Strip(strings.ReplaceAll(text, "\r\n", "\n"))
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
}

func inspectionDiff(target inspectionTarget, diffs []services.ResourceDiff) string {
	var out strings.Builder
	matched := target.Application
	for _, d := range diffs {
		if !target.Application && (d.Group != target.Group || d.Kind != target.Kind || d.Namespace != target.Namespace || d.Name != target.Name) {
			continue
		}
		matched = true
		if d.Hook {
			continue
		}
		live, desired := cleanManifestToYAML(d.NormalizedLiveState), cleanManifestToYAML(d.PredictedLiveState)
		if live == desired {
			continue
		}
		left, right, cleanup, err := writeDiffFiles([]string{live}, []string{desired})
		if err != nil {
			return "  Unavailable: " + err.Error() + "\n"
		}
		diff, err := unifiedDiff(left, right)
		cleanup()
		if err != nil {
			return "  Unavailable: " + err.Error() + "\n"
		}
		fmt.Fprintf(&out, "\n%s/%s (%s)\n%s\n", d.Kind, d.Name, d.Namespace, inspectionDiffLabels(diff))
	}
	if out.Len() > 0 {
		return out.String()
	}
	if !matched {
		return "  No desired state available for this runtime resource.\n"
	}
	return "  No changes.\n"
}

func inspectionDiffLabels(diff string) string {
	lines := strings.Split(diff, "\n")
	// Only the two file header lines contain paths; resource content may itself
	// contain lines beginning with --- or +++ and must remain untouched.
	if len(lines) >= 2 && strings.HasPrefix(lines[0], "--- ") && strings.HasPrefix(lines[1], "+++ ") {
		lines[0], lines[1] = "--- live", "+++ desired"
	}
	return strings.Join(lines, "\n")
}
