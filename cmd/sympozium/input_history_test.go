package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func historyModel() tuiModel {
	m := newTUIModel("default")
	m.inputFocused = true
	m.input.Focus()
	return m
}

func press(m tuiModel, k tea.KeyType) tuiModel {
	next, _ := m.Update(tea.KeyMsg{Type: k})
	return next.(tuiModel)
}

func submit(m tuiModel, text string) tuiModel {
	m.input.SetValue(text)
	m = press(m, tea.KeyEnter)
	// handleCommand blurs the command bar; the user re-focuses it before the next command.
	m.inputFocused = true
	m.input.Focus()
	return m
}

func TestInputHistory_EmptyHistoryLeavesInputUnchanged(t *testing.T) {
	m := historyModel()
	m.input.SetValue("draft")
	for _, k := range []tea.KeyType{tea.KeyUp, tea.KeyCtrlP, tea.KeyDown, tea.KeyCtrlN} {
		m = press(m, k)
		if got := m.input.Value(); got != "draft" {
			t.Fatalf("key %v with empty history changed input to %q", k, got)
		}
	}
}

func TestInputHistory_UpRecallsAndDownReturnsToEmpty(t *testing.T) {
	m := historyModel()
	m = submit(m, "/agents")
	m = submit(m, "/policies")
	m = submit(m, "/policies") // consecutive duplicate is not stored
	m = submit(m, "   ")       // blank is not stored
	m.input.SetValue("")

	steps := []struct {
		key  tea.KeyType
		want string
	}{
		{tea.KeyUp, "/policies"},
		{tea.KeyCtrlP, "/agents"},
		{tea.KeyUp, "/agents"}, // clamps at the oldest entry
		{tea.KeyDown, "/policies"},
		{tea.KeyCtrlN, ""}, // past the newest entry returns to empty
	}
	for i, s := range steps {
		m = press(m, s.key)
		if got := m.input.Value(); got != s.want {
			t.Fatalf("step %d (key %v): input = %q, want %q", i, s.key, got, s.want)
		}
	}
}

func TestInputHistory_DownOutsideBrowsingKeepsDraft(t *testing.T) {
	m := historyModel()
	m = submit(m, "/agents")
	m.input.SetValue("/run agent do something")
	for _, k := range []tea.KeyType{tea.KeyDown, tea.KeyCtrlN} {
		m = press(m, k)
		if got := m.input.Value(); got != "/run agent do something" {
			t.Fatalf("key %v outside history browsing changed draft to %q", k, got)
		}
	}
}

func TestInputHistory_DownPastNewestRestoresDraft(t *testing.T) {
	m := historyModel()
	m = submit(m, "/agents")
	m = submit(m, "/policies")
	m.input.SetValue("/run agent do something")

	m = press(m, tea.KeyUp)
	m = press(m, tea.KeyUp)
	if got := m.input.Value(); got != "/agents" {
		t.Fatalf("input = %q, want /agents", got)
	}
	m = press(m, tea.KeyDown)
	m = press(m, tea.KeyDown)
	if got := m.input.Value(); got != "/run agent do something" {
		t.Fatalf("input = %q, want draft restored", got)
	}
}

func TestInputHistory_SuggestionsKeepNavigatingSuggestions(t *testing.T) {
	m := historyModel()
	m = submit(m, "/agents")
	m.input.SetValue("/a")
	m.suggestions = []suggestion{{}, {}}
	m.suggestIdx = 0

	m = press(m, tea.KeyUp)
	if m.suggestIdx != 1 {
		t.Fatalf("suggestIdx = %d, want 1", m.suggestIdx)
	}
	if got := m.input.Value(); got != "/a" {
		t.Fatalf("input = %q, want unchanged %q", got, "/a")
	}
}
