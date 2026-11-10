package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/darksworm/argonaut/pkg/api"
	"github.com/darksworm/argonaut/pkg/model"
	"github.com/darksworm/argonaut/pkg/services"
	"gopkg.in/yaml.v3"
)

func TestInspectionYAMLPreservesFullObject(t *testing.T) {
	object, err := decodeInspectionObject(json.RawMessage(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"uid":"keep","resourceVersion":"123","managedFields":[{"manager":"controller"}]},"data":{"on":"true","multiline":"one\ntwo\n"},"status":{"extra":9007199254740993}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	text, err := inspectionYAML(object)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"managedFields:", "resourceVersion:", "uid:", "9007199254740993", "status:", "multiline:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("YAML lost %q:\n%s", want, text)
		}
	}
	var decoded map[string]any
	if err := yaml.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatal(err)
	}
	data := decoded["data"].(map[string]any)
	if data["on"] != "true" || data["multiline"] != "one\ntwo\n" {
		t.Fatalf("YAML changed string semantics: %s", text)
	}
	for _, bad := range []string{"null", "[]", "true", "42"} {
		if _, err := decodeInspectionObject(json.RawMessage(bad), false); err == nil {
			t.Errorf("accepted non-object %s", bad)
		}
	}
}

func TestDescribeContainsObjectEventsAndExactResourceDiff(t *testing.T) {
	m := buildEventsPaneTestModel()
	m.treeView.SetSelectedIndex(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("appNamespace") != "test-namespace" {
			t.Errorf("lost app identity: %s", r.URL)
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/resource"):
			if r.URL.Query().Get("resourceName") != "web-1" || r.URL.Query().Get("namespace") != "demo" {
				t.Errorf("wrong resource: %s", r.URL)
			}
			json.NewEncoder(w).Encode(map[string]string{"manifest": `{"apiVersion":"v1","kind":"Pod","metadata":{"name":"web-1","namespace":"demo","uid":"pod-uid"},"spec":{"containers":[{"name":"web","image":"example:v1"}]},"status":{"phase":"Running"}}`})
		case strings.HasSuffix(r.URL.Path, "/events"):
			if r.URL.Query().Get("resourceUID") != "pod-uid" {
				t.Errorf("wrong events target: %s", r.URL)
			}
			w.Write([]byte(`{"items":[{"type":"Normal","reason":"Started","message":"container started","count":2}]}`))
		case strings.HasSuffix(r.URL.Path, "/managed-resources"):
			json.NewEncoder(w).Encode(api.ManagedResourcesResponse{Items: []api.ManagedResourceDiff{
				{Kind: "Pod", Name: "web-1", Namespace: "other", NormalizedLiveState: `{"data":{"wrong":"wrong-namespace"}}`, PredictedLiveState: `{"data":{"wrong":"leaked"}}`},
				{Kind: "Pod", Name: "web-1", Namespace: "demo", NormalizedLiveState: `{"spec":{"image":"example:v1"}}`, PredictedLiveState: `{"spec":{"image":"example:v2"}}`},
			}})
		default:
			t.Errorf("unexpected request: %s", r.URL)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	m.state.Server = &model.Server{BaseURL: server.URL}
	_, cmd := m.handleKeyMsg(testKeyMsg("d"))
	if cmd == nil || m.state.Mode != model.ModeDiff {
		t.Fatal("d must open describe")
	}
	msg := cmd().(resourceInspectionMsg)
	if msg.Error != nil {
		t.Fatal(msg.Error)
	}
	m.Update(msg)
	text := strings.Join(m.state.Diff.Content, "\n") + "\n" + strings.Join(m.state.Diff.SideContent, "\n")
	for _, want := range []string{"Name:", "web-1", "Containers:", "Running", "Events:", "Started", "-    image: example:v1", "+    image: example:v2"} {
		if !strings.Contains(text, want) {
			t.Fatalf("describe missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "wrong-namespace") || strings.Contains(text, "leaked") {
		t.Fatal("diff mixed resource identities")
	}
	if len(m.state.Diff.SideContent) == 0 {
		t.Fatal("diff must have its own panel")
	}
	m.treeView.SetSelectedIndex(0)
	_, _ = m.handleKeyMsg(testKeyMsg("y"))
	if m.inspection.Target.Name != "web-1" || m.inspection.Kind != "YAML" {
		t.Fatal("switching inspection must retain the captured target")
	}
	m.cancelResourceInspection()
}

func TestInspectionCancelsRequestsAndGatesLateReplies(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done(); close(cancelled) }))
	defer server.Close()
	m := buildDeleteTestModel(100, 30)
	m.state.Server = &model.Server{BaseURL: server.URL}
	_, cmd := m.handleKeyMsg(testKeyMsg("y"))
	result := make(chan resourceInspectionMsg, 1)
	go func() { result <- cmd().(resourceInspectionMsg) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	m.handleKeyMsg(testKeyMsg("esc"))
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("Esc did not cancel API request")
	}
	msg := <-result
	m.Update(msg)
	if m.inspection != nil || m.state.Diff != nil || m.state.Mode != model.ModeNormal {
		t.Fatal("late response reopened inspection")
	}

	for _, variant := range []string{"epoch", "sequence", "target"} {
		t.Run(variant, func(t *testing.T) {
			_, _ = m.handleKeyMsg(testKeyMsg("y"))
			st := m.inspection
			msg := resourceInspectionMsg{Target: st.Target, SwitchEpoch: m.switchEpoch, Sequence: st.Sequence, Text: "stale"}
			switch variant {
			case "epoch":
				msg.SwitchEpoch--
			case "sequence":
				msg.Sequence--
			case "target":
				msg.Target.AppNamespace = "other"
			}
			before := append([]string(nil), m.state.Diff.Content...)
			m.Update(msg)
			if !reflect.DeepEqual(before, m.state.Diff.Content) || !m.state.Diff.Loading {
				t.Fatal("accepted stale inspection")
			}
			m.Update(textPagerMsg{Title: "unrelated old diff", Text: "stale", SwitchEpoch: m.switchEpoch})
			m.Update(model.SetModeMsg{Mode: model.ModeNormal})
			if m.state.Mode != model.ModeDiff || !reflect.DeepEqual(before, m.state.Diff.Content) {
				t.Fatal("background completion dismissed inspection")
			}
			m.cancelResourceInspection()
			m.state.Mode = model.ModeNormal
			m.state.Diff = nil
		})
	}
}

func TestInspectionRuntimeDiffAndTerminalText(t *testing.T) {
	if text := inspectionDiff(inspectionTarget{Kind: "Pod", Name: "runtime"}, []services.ResourceDiff{}); !strings.Contains(text, "No desired state") {
		t.Fatal(text)
	}
	if text := inspectionPlainText("safe\x1b]52;c;Zm9v\a\x1b[31mred\x00\r\nnext"); text != "safered\nnext" {
		t.Fatalf("terminal control sequence was not removed: %q", text)
	}
}

func TestActionShortcutsRequireCtrl(t *testing.T) {
	for _, key := range []string{"s", "t", "R", "a", "K", "L"} {
		m := buildTerminateTestModel("Running")
		_, cmd := m.handleKeyMsg(testKeyMsg(key))
		if cmd != nil || m.state.Mode != model.ModeNormal {
			t.Fatalf("plain %s triggered an operation", key)
		}
	}
	for key, mode := range map[string]model.Mode{"ctrl+s": model.ModeConfirmSync, "ctrl+d": model.ModeConfirmAppDelete, "ctrl+t": model.ModeConfirmTerminate} {
		m := buildTerminateTestModel("Running")
		m.handleKeyMsg(testKeyMsg(key))
		if m.state.Mode != mode {
			t.Fatalf("%s: got %s want %s", key, m.state.Mode, mode)
		}
	}
	m := buildDeleteTestModel(100, 30)
	_, cmd := m.handleKeyMsg(testKeyMsg("ctrl+r"))
	if cmd == nil {
		t.Fatal("Ctrl+R must refresh the application")
	}
}
