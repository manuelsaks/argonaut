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
	if err != nil {
		message := "Cannot load Argo CD contexts: " + err.Error()
		m.statusService.Error(message)
		return m, m.showFooterNotice(message)
	}
	m.contextSwitchSeq++
	m.state.ContextNames = cfg.GetContextNames()
	m.state.Mode = model.ModeNormal
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
	m.listNav.SetItemCount(len(m.state.ContextNames))
	m.listNav.SetViewportHeight(m.listViewportHeight())
	m.listNav.SetCursor(m.state.Navigation.SelectedIdx)
	return m, nil
}

func (m *Model) closeContextPicker() (tea.Model, tea.Cmd) {
	m.contextSwitchSeq++ // Cancel a switch whose result has not arrived yet.
	m.state.UI.SearchQuery = ""
	m.state.UI.ActiveFilter = ""
	m = m.safeChangeView(model.ViewApps)
	m.state.Navigation.SelectedIdx = 0
	return m, nil
}

// performContextSwitch re-reads the ArgoCD config and resolves the named context.
// Returns a ContextSwitchResultMsg with the new server config or an error.
func (m *Model) performContextSwitch(contextName string) tea.Cmd {
	epoch := m.switchEpoch
	m.contextSwitchSeq++
	seq := m.contextSwitchSeq
	configPath := m.argoConfigPath
	if configPath == "" {
		configPath = config.GetConfigPath()
	}
	currentCtx := m.currentContextName
	return func() tea.Msg {
		result := model.ContextSwitchResultMsg{ContextName: contextName, SwitchEpoch: epoch, RequestID: seq}
		// Same-context no-op
		if contextName == currentCtx {
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
	if msg.ContextName == m.currentContextName {
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

	// 5. Start fresh load cycle
	return newM, tea.Batch(
		newM.spinner.Tick,
		func() tea.Msg { return model.SetInitialLoadingMsg{Loading: true} },
		newM.validateAuthentication(),
	)
}
