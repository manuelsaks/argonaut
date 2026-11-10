package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	apperrors "github.com/darksworm/argonaut/pkg/errors"
	"github.com/darksworm/argonaut/pkg/model"
)

func TestStartupOpensContextsBeforeNetworkAndStartsEnabledSSO(t *testing.T) {
	for _, auto := range []bool{false, true} {
		m := authTestModel(t)
		m.config.Network.LowBandwidth = true
		m.config.Auth.AutoRelogin["prod"] = auto
		m.state.Mode = model.ModeLoading
		m.state.Modals.InitialLoading = true
		m.Init()
		if m.state.Navigation.View != model.ViewContexts || m.state.Mode != model.ModeNormal || m.state.Modals.InitialLoading || !m.contextNeedsLoad {
			t.Fatal("startup must synchronously expose the context picker")
		}
		if m.reloginInProgress != auto {
			t.Fatal("startup did not honor the context's automatic SSO preference")
		}
		if !strings.Contains(stripANSI(m.renderBanner()), "<ctrl-c> quit") {
			t.Fatal("startup must advertise an available exit instead of returning to an unloaded view")
		}
		m.handleKeyMsg(testKeyMsg("k"))
		if m.state.Navigation.SelectedIdx != 0 {
			t.Fatal("startup picker cannot be navigated while login is pending")
		}
		m.cancelContextRelogin()
	}
}

func TestUnauthorizedAlwaysRecoversToUsableContextPicker(t *testing.T) {
	for _, auto := range []bool{false, true} {
		for _, source := range []string{"validation", "structured", "api", "auth", "mode"} {
			t.Run(source+map[bool]string{true: "/auto", false: "/manual"}[auto], func(t *testing.T) {
				m := authTestModel(t)
				m.config.Auth.AutoRelogin["prod"] = auto
				m.state.Mode = model.ModeLoading
				m.state.Modals.InitialLoading = true
				m.err = errors.New("old unauthorized error")
				var message tea.Msg
				switch source {
				case "validation":
					message = model.AuthValidationResultMsg{Mode: model.ModeAuthRequired, NeedsLogin: true}
				case "structured":
					message = model.StructuredErrorMsg{Error: apperrors.New(apperrors.ErrorAuth, "UNAUTHORIZED", "session expired").WithSeverity(apperrors.SeverityHigh)}
				case "api":
					message = model.ApiErrorMsg{StatusCode: 401, Message: "unauthorized"}
				case "auth":
					message = model.AuthErrorMsg{Error: errors.New("401 unauthorized")}
				case "mode":
					message = model.SetModeMsg{Mode: model.ModeAuthRequired}
				}
				m.Update(message)
				if m.state.Navigation.View != model.ViewContexts || m.state.Mode != model.ModeNormal || m.err != nil || m.state.Modals.InitialLoading || m.activeOverlay() != nil {
					t.Fatal("authentication failure trapped the user behind an error/loading view")
				}
				if m.reloginInProgress != auto {
					t.Fatal("authentication failure bypassed the saved SSO preference")
				}
				m.handleKeyMsg(testKeyMsg("k"))
				if m.state.Navigation.SelectedIdx != 0 {
					t.Fatal("authentication failure blocked context navigation")
				}
				m.cancelContextRelogin()
			})
		}
	}
}

func TestFailedOrUnavailableSSOLeavesContextPickerUsable(t *testing.T) {
	m := authTestModel(t)
	m.config.Auth.Command = filepath.Join(t.TempDir(), "missing-argocd.exe")
	m.config.Network.LowBandwidth = true
	m.Init()
	// Dispatch the same missing-CLI error the startup command produces.
	m.Update(contextReloginMsg{ContextName: "prod", SwitchEpoch: m.switchEpoch, RequestID: m.reloginSeq, Error: errors.New("Argo CD CLI unavailable")})
	if m.state.Mode != model.ModeNormal || m.state.Navigation.View != model.ViewContexts || m.reloginInProgress || !strings.Contains(m.state.UI.FooterNotice, "CLI unavailable") {
		t.Fatal("failed SSO must keep the picker visible with an actionable notice")
	}
	m.handleKeyMsg(testKeyMsg("k"))
	if m.state.Navigation.SelectedIdx != 0 {
		t.Fatal("cannot select another context after SSO fails")
	}
	m.handleKeyMsg(testKeyMsg("esc"))
	if m.state.Navigation.View != model.ViewContexts {
		t.Fatal("Esc must not return to an unauthenticated Applications view")
	}
}

func TestStartupSameContextSelectionLoadsInsteadOfNoOp(t *testing.T) {
	m := authTestModel(t)
	m.config.Auth.AutoRelogin["prod"] = false
	m.config.Network.LowBandwidth = true
	m.Init()
	_, cmd := m.handleKeyMsg(testKeyMsg("enter"))
	msg := cmd().(model.ContextSwitchResultMsg)
	updated, load := m.Update(msg)
	if updated == m || load == nil || updated.(*Model).state.Server == nil || updated.(*Model).state.Navigation.View == model.ViewContexts {
		t.Fatal("Enter on the initial current-context row must start its load")
	}
}

func TestSelectingAnotherContextCancelsPendingSSOAndRejectsItsReply(t *testing.T) {
	m := authTestModel(t)
	m.openContextPicker()
	login := m.beginContextRelogin("prod", false)
	switchContext := m.performContextSwitch("dev")
	if m.reloginInProgress {
		t.Fatal("choosing another context must cancel the previous SSO immediately")
	}
	updated, _ := m.Update(switchContext())
	next := updated.(*Model)
	late := login().(contextReloginMsg)
	if late.Error == nil {
		t.Fatal("cancelled login subprocess unexpectedly ran")
	}
	result, cmd := next.Update(late)
	if result != next || cmd != nil || next.currentContextName != "dev" || next.state.Server.Token != "dev-test-token" {
		t.Fatal("late SSO completion changed the chosen context")
	}
}

type startupProbeMsg struct{ Reply chan startupSnapshot }
type startupSnapshot struct {
	View    model.View
	Mode    model.Mode
	Row     int
	Busy    bool
	Renewed bool
}
type responsiveSSOModel struct{ *Model }

func (m *responsiveSSOModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := message.(startupProbeMsg); ok {
		msg.Reply <- startupSnapshot{View: m.state.Navigation.View, Mode: m.state.Mode, Row: m.state.Navigation.SelectedIdx, Busy: m.reloginInProgress, Renewed: m.state.Server != nil && m.state.Server.Token == "renewed-test-token"}
		return m, nil
	}
	updated, cmd := m.Model.Update(message)
	m.Model = updated.(*Model)
	return m, cmd
}

func probeStartup(t *testing.T, p *tea.Program) startupSnapshot {
	t.Helper()
	reply := make(chan startupSnapshot, 1)
	go p.Send(startupProbeMsg{Reply: reply})
	select {
	case snapshot := <-reply:
		return snapshot
	case <-time.After(2 * time.Second):
		t.Fatal("Bubble Tea stopped handling messages while the SSO process waited")
		return startupSnapshot{}
	}
}

func sendStartupKey(t *testing.T, p *tea.Program, key string) {
	t.Helper()
	sent := make(chan struct{})
	go func() { p.Send(testKeyMsg(key)); close(sent) }()
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatalf("key %s blocked while SSO was waiting", key)
	}
}

func TestBrowserSSOWaitDoesNotSuspendRealTUIEventLoop(t *testing.T) {
	m := authTestModel(t)
	m.config.Network.LowBandwidth = true
	waitDir := t.TempDir()
	t.Setenv("ARGONAUT_TEST_CLI_USERINFO", `{"loggedIn":false}`)
	t.Setenv("ARGONAUT_TEST_CLI_WAIT_DIR", waitDir)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	p := tea.NewProgram(&responsiveSSOModel{Model: m}, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()
	defer func() {
		_ = os.WriteFile(filepath.Join(waitDir, "continue"), []byte("release"), 0600)
		p.Kill()
	}()
	for {
		if _, err := os.Stat(filepath.Join(waitDir, "started")); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("TUI exited before starting browser SSO: %v", err)
		case <-ctx.Done():
			t.Fatal("SSO subprocess never started")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if got := probeStartup(t, p); got.View != model.ViewContexts || got.Mode != model.ModeNormal || !got.Busy {
		t.Fatalf("SSO should wait behind the usable context picker: %+v", got)
	}
	sendStartupKey(t, p, "k")
	if got := probeStartup(t, p); got.Row != 0 || !got.Busy {
		t.Fatalf("cannot select another context during SSO: %+v", got)
	}
	sendStartupKey(t, p, "?")
	if got := probeStartup(t, p); got.Mode != model.ModeHelp || !got.Busy {
		t.Fatalf("cannot change views while SSO waits: %+v", got)
	}
	sendStartupKey(t, p, "esc")
	sendStartupKey(t, p, "0")
	if got := probeStartup(t, p); !got.Busy || got.View != model.ViewContexts {
		t.Fatalf("SSO stole focus or stopped during navigation: %+v", got)
	}
	sendStartupKey(t, p, "ctrl+c")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("quit failed while SSO was pending: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSO subprocess prevented quitting")
	}
}
