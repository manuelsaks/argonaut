package main

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/darksworm/argonaut/pkg/model"
	"github.com/darksworm/argonaut/pkg/tui/clipboard"
)

func TestAllContextsQueuesEveryEnabledSSOSession(t *testing.T) {
	m := authTestModel(t)
	m.config.Auth.AutoRelogin["dev"] = true
	updated, cmd := m.openAllContexts()
	m = updated.(*Model)
	t.Cleanup(m.cancelContextRelogin)
	t.Cleanup(m.cancelAllContextRequests)
	if m.reloginContext != "dev" || !slices.Equal(m.reloginQueue, []string{"prod"}) {
		t.Fatalf("SSO queue lost a context: active=%s queue=%v", m.reloginContext, m.reloginQueue)
	}
	// Concurrent failures from applications, AppSets and destinations must
	// coalesce into one browser login per context.
	for range 3 {
		if _, handled := m.maybeAutoReloginForContext("prod"); !handled {
			t.Fatal("queued login was reported as unhandled")
		}
	}
	if len(m.reloginQueue) != 1 {
		t.Fatal("duplicate SSO callbacks were queued")
	}
	first := cmd().(contextReloginMsg)
	if first.Error != nil || first.ContextName != "dev" {
		t.Fatalf("first CLI refresh failed: %+v", first)
	}
	_, next := m.Update(first)
	if m.reloginContext != "prod" || next == nil || !m.reloginValidated["dev"] {
		t.Fatal("second context did not start after the first completed")
	}
	second := next().(contextReloginMsg)
	if second.Error != nil || second.ContextName != "prod" {
		t.Fatalf("second CLI refresh failed: %+v", second)
	}
	_, reload := m.Update(second)
	if reload == nil || m.reloginInProgress || len(m.reloginQueue) != 0 || !m.reloginValidated["prod"] {
		t.Fatal("queue did not drain and refresh the aggregate view")
	}
	if len(m.allContexts.Pending) != 2 || len(m.allContexts.AppSetPending) != 2 || m.currentContextName != "all" {
		t.Fatal("all must reload both contexts and their AppSets after SSO")
	}
}

func TestSSOFailureDoesNotDiscardNextContext(t *testing.T) {
	m := authTestModel(t)
	m.config.Auth.AutoRelogin["dev"] = true
	_, _ = m.maybeAutoReloginForContext("dev")
	_, _ = m.maybeAutoReloginForContext("prod")
	_, cmd := m.Update(contextReloginMsg{ContextName: "dev", SwitchEpoch: m.switchEpoch, RequestID: m.reloginSeq, Error: errors.New("cancelled")})
	if cmd == nil || !m.reloginInProgress || m.reloginContext != "prod" {
		t.Fatal("one failed SSO must not cancel another context's login")
	}
	m.cancelContextRelogin()
}

func TestSelectedContextAuthenticatesBeforeLoadingEvenWithoutToken(t *testing.T) {
	for _, name := range []string{"dev", "prod"} {
		t.Run(name, func(t *testing.T) {
			m := authTestModel(t)
			m.config.Auth.AutoRelogin[name] = true
			data, err := os.ReadFile(m.argoConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			data = []byte(strings.ReplaceAll(string(data), "auth-token: "+name+"-test-token", "auth-token: ''"))
			if err := os.WriteFile(m.argoConfigPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			msg := m.performContextSwitch(name)().(model.ContextSwitchResultMsg)
			if msg.Error != nil || msg.Server == nil || msg.Server.Token != "" {
				t.Fatalf("logged-out context cannot be selected: %+v", msg)
			}
			updated, cmd := m.Update(msg)
			next := updated.(*Model)
			defer next.cancelContextRelogin()
			if cmd == nil || next.currentContextName != name || !next.reloginInProgress || next.state.Mode == model.ModeAuthRequired {
				t.Fatal("selection should authenticate without showing not logged in")
			}
			login, err := argoCDCommand(t.Context(), next.config.GetArgoCDCommand(), next.argoConfigPath, name, "login")
			if err != nil || !slices.Contains(login.Args, "--sso") || !slices.Contains(login.Args, "--name") {
				t.Fatal("missing-token login must use context-bound browser SSO")
			}
		})
	}
}

func TestSuccessfulValidationAllowsAnotherImmediateSessionRenewal(t *testing.T) {
	m := authTestModel(t)
	cmd, _ := m.maybeAutoRelogin()
	updated, _ := m.Update(cmd())
	m = updated.(*Model)
	m.Update(model.AuthValidationResultMsg{Mode: model.ModeLoading, SwitchEpoch: m.switchEpoch})
	_, cmd = m.Update(model.AuthErrorMsg{Error: errors.New("not logged in"), SwitchEpoch: m.switchEpoch})
	if cmd == nil || !m.reloginInProgress {
		t.Fatal("a previously successful login must not block renewal of a newly expired session")
	}
	m.cancelContextRelogin()
}

func TestAnonymousAPIResponseAfterSuccessfulCLIProbeStartsBrowserSSO(t *testing.T) {
	m := authTestModel(t)
	cmd, _ := m.maybeAutoRelogin()
	updated, _ := m.Update(cmd())
	m = updated.(*Model)
	_, login := m.Update(model.AuthValidationResultMsg{Mode: model.ModeAuthRequired, NeedsLogin: true, SwitchEpoch: m.switchEpoch})
	if login == nil || !m.reloginInProgress || m.state.Mode == model.ModeAuthRequired || m.reloginProbed["prod"] {
		t.Fatal("CLI exit 0 must not suppress SSO when the API reports loggedIn=false")
	}
	m.cancelContextRelogin()
}

func TestSilentCLIProbeDetectsLoggedOutSessionsAndMissingCredentials(t *testing.T) {
	for _, missingToken := range []bool{false, true} {
		t.Run(map[bool]string{false: "anonymous-userinfo", true: "missing-token"}[missingToken], func(t *testing.T) {
			m := authTestModel(t)
			body := `{"loggedIn":false}`
			if missingToken {
				body = `{"loggedIn":true}`
				data, err := os.ReadFile(m.argoConfigPath)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(m.argoConfigPath, []byte(strings.ReplaceAll(string(data), "prod-test-token", "")), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("ARGONAUT_TEST_CLI_USERINFO", body)
			msg := m.beginContextRelogin("prod", true)().(contextReloginMsg)
			if missingToken {
				if msg.Error != nil || msg.Server == nil || msg.Server.Token != "renewed-test-token" || msg.Probed {
					t.Fatalf("missing credentials must start full SSO immediately: %+v", msg)
				}
			} else if msg.Error != nil || !msg.Interactive || msg.Probed {
				t.Fatalf("logged-out CLI exit 0 must request browser SSO: %+v", msg)
			}
			m.cancelContextRelogin()
		})
	}
}

func TestAppSetsExistWithoutGeneratedApplicationsAndRemainContextScoped(t *testing.T) {
	m := allContextsTestModel(t)
	m.state.Navigation.View = model.ViewApplicationSets
	if got := len(m.getVisibleItems()); got != 4 {
		t.Fatalf("sets without children disappeared or contexts collided: %d", got)
	}
	for _, contextName := range []string{"dev", "prod"} {
		key := model.ContextKey(contextName, "argocd/empty-set")
		if !slices.Contains(m.state.Index.ApplicationSets, key) || len(m.state.Index.ByApplicationSet[key]) != 0 {
			t.Fatalf("empty set lost: %s", key)
		}
	}
	m.allContexts.AppSetPending["prod"] = true
	before := slices.Clone(m.state.ApplicationSets)
	m.Update(applicationSetsMsg{SwitchEpoch: m.switchEpoch - 1, Sequence: m.allContexts.Sequence, ContextName: "prod"})
	if !slices.Equal(before, m.state.ApplicationSets) || !m.allContexts.AppSetPending["prod"] {
		t.Fatal("stale AppSet reply changed current state")
	}
}

func TestAuxiliaryArgoReadsAlsoRenewSessions(t *testing.T) {
	for _, source := range []string{"servers", "appsets", "delete", "resource-sync", "refresh", "tree-watch"} {
		t.Run(source, func(t *testing.T) {
			m := authTestModel(t)
			var message tea.Msg
			switch source {
			case "servers":
				message = destinationServersMsg{Error: errors.New("401 unauthenticated")}
			case "appsets":
				m.applicationSets = &applicationSetsState{}
				message = applicationSetsMsg{Error: errors.New("401 unauthenticated")}
			case "delete":
				message = model.AppDeleteErrorMsg{AppName: "web", Error: "401 unauthenticated"}
			case "resource-sync":
				message = model.ResourceSyncErrorMsg{Error: "401 unauthenticated"}
			case "refresh":
				message = model.RefreshCompletedMsg{Error: errors.New("401 unauthenticated")}
			case "tree-watch":
				m.state.Navigation.View = model.ViewTree
				message = treeWatchStartedMsg{err: errors.New("401 unauthenticated")}
			}
			_, cmd := m.Update(message)
			if cmd == nil || !m.reloginInProgress || m.state.Mode == model.ModeAuthRequired {
				t.Fatal("API failure bypassed auto SSO")
			}
			m.cancelContextRelogin()
		})
	}
}

func TestClipboardReportsActualCompletionAndCopiesDocumentWithoutMouse(t *testing.T) {
	m := buildDeleteTestModel(100, 30)
	m.state.Diff = &model.DiffState{InspectionKind: "YAML", Content: []string{"apiVersion: v1", "kind: Pod"}}
	m.state.Mode = model.ModeDiff
	_, cmd := m.handleKeyMsg(testKeyMsg("ctrl+y"))
	if cmd == nil || m.state.UI.SelectionCopied {
		t.Fatal("copy should be asynchronous and only report success after completion")
	}
	m.Update(clipboard.CopyMsg{Success: false})
	if m.state.UI.SelectionCopied || !strings.Contains(m.state.UI.FooterNotice, "Copy failed") {
		t.Fatal("native clipboard failure was hidden")
	}
	m.Update(clipboard.CopyMsg{Success: true, Method: "native"})
	if !m.state.UI.SelectionCopied {
		t.Fatal("successful native copy was not reported")
	}
}
