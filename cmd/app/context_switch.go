package main

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	cblog "github.com/charmbracelet/log"
	"github.com/darksworm/argonaut/pkg/config"
	"github.com/darksworm/argonaut/pkg/model"
)

// openContextPicker lists Argo CD CLI contexts, independent of kubeconfig.
// Re-read the file so logins added by the CLI are available without a restart.
func (m *Model) openContextPicker() (tea.Model, tea.Cmd) {
	configPath := m.argoConfigPath
	if configPath == "" {
		configPath = config.GetConfigPath()
	}
	cfg, err := config.ReadCLIConfigFromPath(configPath)
	m.contextSwitchSeq++
	if err == nil {
		m.state.ContextNames = cfg.GetContextNames()
	}
	m.state.Mode = model.ModeNormal
	m.err = nil
	m.state.CurrentError = nil
	if m.state.ErrorState != nil {
		m.state.ErrorState.Current = nil
	}
	m.state.Modals = model.ModalState{}
	m.cancelResourceInspection()
	m.state.Diff = nil
	m.selection.Clear()
	m.selectionSnapshot, m.selectedText = "", ""
	m.inputComponents.BlurInputs()
	m.state.UI.SearchQuery = ""
	m.state.UI.ActiveFilter = ""
	m = m.safeChangeView(model.ViewContexts)
	m.clearTreeApp()
	m.treeLoading = false
	m.rollbackWatchApp = nil
	m.state.SavedNavigation = nil
	m.state.Navigation.SelectedIdx = 0
	for i, name := range m.state.ContextNames {
		if name == m.currentContextName {
			m.state.Navigation.SelectedIdx = i
			break
		}
	}
	if m.allContexts != nil {
		m.state.Navigation.SelectedIdx = len(m.state.ContextNames)
	}
	m.listNav.SetItemCount(len(m.getVisibleItems()))
	m.listNav.SetViewportHeight(m.listViewportHeight())
	m.listNav.SetCursor(m.state.Navigation.SelectedIdx)
	if err != nil {
		message := "Cannot load Argo CD contexts: " + err.Error()
		m.statusService.Error(message)
		return m, m.showFooterNotice(message)
	}
	return m, nil
}

func (m *Model) closeContextPicker() (tea.Model, tea.Cmd) {
	m.contextSwitchSeq++ // Cancel a switch whose result has not arrived yet.
	if m.contextNeedsLoad {
		return m, m.showFooterNotice("Select an Argo CD context; Ctrl+L: SSO login")
	}
	m.state.UI.SearchQuery = ""
	m.state.UI.ActiveFilter = ""
	m = m.safeChangeView(model.ViewApps)
	m.state.Navigation.SelectedIdx = 0
	return m, nil
}

// performContextSwitch re-reads the ArgoCD config and resolves the named context.
// Returns a ContextSwitchResultMsg with the new server config or an error.
func (m *Model) performContextSwitch(contextName string) tea.Cmd {
	// A login completing while this selection resolves must not steal focus
	// back or invalidate the user's context-switch result.
	m.cancelContextRelogin()
	epoch := m.switchEpoch
	m.contextSwitchSeq++
	seq := m.contextSwitchSeq
	configPath := m.argoConfigPath
	if configPath == "" {
		configPath = config.GetConfigPath()
	}
	currentCtx := m.currentContextName
	fromOverview := m.allContexts != nil
	autoLogin := m.config.IsAutoReloginEnabled(contextName)
	needsLoad := m.contextNeedsLoad
	return func() tea.Msg {
		result := model.ContextSwitchResultMsg{ContextName: contextName, SwitchEpoch: epoch, RequestID: seq}
		// Same-context no-op
		if contextName == currentCtx && !fromOverview && !autoLogin && !needsLoad {
			return result
		}

		// Re-read config from disk (tokens may have changed)
		cfg, err := config.ReadCLIConfigFromPath(configPath)
		if err != nil {
			result.Error = err
			return result
		}

		// Check for unsupported modes
		if isPF, pfErr := cfg.IsContextPortForward(contextName); pfErr == nil && isPF {
			result.Error = fmt.Errorf("context %q uses port-forward mode, which is not supported for live switching", contextName)
			return result
		}
		if isCore, coreErr := cfg.IsContextCore(contextName); coreErr == nil && isCore {
			result.Error = fmt.Errorf("context %q uses core mode, which is not supported for live switching", contextName)
			return result
		}

		// Resolve context to Server
		server, err := cfg.ToServerConfigForContext(contextName)
		if autoLogin {
			server, err = cfg.ConnectionForContext(contextName)
		}
		if err != nil {
			result.Error = err
			return result
		}

		result.Server = server
		result.ContextNames = cfg.GetContextNames()
		return result
	}
}

// handleContextSwitchResult processes the result of a context switch.
// It tears down the old model, creates a fresh one via NewModel(), and
// transfers only the narrow set of fields that must survive across contexts.
func (m *Model) handleContextSwitchResult(msg model.ContextSwitchResultMsg) (tea.Model, tea.Cmd) {
	if msg.SwitchEpoch != m.switchEpoch || msg.RequestID != m.contextSwitchSeq {
		return m, nil
	}
	if msg.Error != nil {
		message := "Context switch failed: " + msg.Error.Error()
		m.statusService.Error(message)
		return m, m.showFooterNotice(message)
	}
	if msg.ContextName == m.currentContextName && !msg.Reload && m.allContexts == nil && !m.config.IsAutoReloginEnabled(msg.ContextName) && !m.contextNeedsLoad {
		m.statusService.Set("Already on context: " + msg.ContextName)
		if m.state.Navigation.View == model.ViewContexts {
			return m.closeContextPicker()
		}
		return m, nil
	}

	cblog.With("component", "context-switch").Info("Switching context",
		"from", m.currentContextName,
		"to", msg.ContextName,
		"server", msg.Server.BaseURL)

	// 1. Cleanup old goroutines — ORDER MATTERS:
	m.cancelAllContextRequests()
	m.cancelContextRelogin()
	m.cancelResourceInspection()
	//    a. Stop forwarding goroutine FIRST
	m.cleanupAppWatcher()
	//    b. Stop tree watchers
	_ = m.cleanupTreeWatchers()

	// 2. Create fresh model with same config (re-applies preferences)
	newM := NewModel(m.config)

	// 3. Re-run Init() side effects that BubbleTea won't call on model swap
	newM.applyThemeToModel()

	// 4. Transfer ONLY what must survive (narrowly defined, test-locked)
	newM.program = m.program                   // BubbleTea program pointer
	newM.state.Terminal = m.state.Terminal     // Current terminal size
	newM.ready = true                          // Already have terminal size
	newM.argoConfigPath = m.argoConfigPath     // For future switches
	newM.currentContextName = msg.ContextName  // New context name
	newM.state.Server = msg.Server             // New server config
	newM.state.ContextNames = msg.ContextNames // From result (no 2nd config read)
	newM.switchEpoch = m.switchEpoch + 1       // Increment epoch
	newM.reloginAttempts = m.reloginAttempts   // Throttle failed login loops across context switches.
	newM.reloginProbed = m.reloginProbed
	if msg.Reload && msg.ContextName == m.currentContextName {
		newM.allOverview = m.allOverview
	}

	// Validate/refresh through the CLI before opening an enabled context.
	// A completed login reload must validate via the API rather than recurse.
	if !msg.Reload {
		delete(newM.reloginAttempts, msg.ContextName)
		delete(newM.reloginProbed, msg.ContextName)
		if login, handled := newM.maybeAutoRelogin(); handled {
			return newM, tea.Batch(newM.spinner.Tick, login)
		}
	}
	// 5. Start fresh load cycle
	return newM, tea.Batch(
		newM.spinner.Tick,
		func() tea.Msg { return model.SetInitialLoadingMsg{Loading: true} },
		newM.validateAuthentication(),
	)
}
