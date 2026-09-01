package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func key(t tea.KeyType) tea.Msg { return tea.KeyMsg{Type: t} }

func TestSelectModelChoosesWithEnter(t *testing.T) {
	m := newSelectModel("Connect a channel", []string{"telegram", "smoke-signals"})

	// move down then back up, then choose
	mm, _ := m.Update(key(tea.KeyDown))
	m = mm.(selectModel)
	if m.cursor != 1 {
		t.Fatalf("cursor after down = %d, want 1", m.cursor)
	}
	mm, _ = m.Update(key(tea.KeyUp))
	m = mm.(selectModel)
	mm, _ = m.Update(key(tea.KeyEnter))
	m = mm.(selectModel)
	if m.choice != "telegram" {
		t.Fatalf("choice = %q, want telegram", m.choice)
	}
}

func TestSelectModelEscCancels(t *testing.T) {
	m := newSelectModel("Connect a channel", []string{"telegram"})
	mm, _ := m.Update(key(tea.KeyEsc))
	m = mm.(selectModel)
	if m.choice != "" {
		t.Fatalf("choice after esc = %q, want empty", m.choice)
	}
}

func TestSelectModelViewShowsItems(t *testing.T) {
	m := newSelectModel("Connect a channel", []string{"telegram"})
	v := m.View()
	if !strings.Contains(v, "telegram") || !strings.Contains(v, "Connect a channel") {
		t.Fatalf("View missing content:\n%s", v)
	}
}

func TestTokenModelCapturesInput(t *testing.T) {
	m := newTokenModel("Secret", "...", "enter confirm · esc cancel")
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("123:abc")})
	m = mm.(tokenModel)
	mm, _ = m.Update(key(tea.KeyEnter))
	m = mm.(tokenModel)
	if m.Token() != "123:abc" {
		t.Fatalf("Token = %q, want 123:abc", m.Token())
	}
}

func TestTokenModelEscCancels(t *testing.T) {
	m := newTokenModel("Secret", "...", "enter confirm · esc cancel")
	mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = mm.(tokenModel)
	mm, _ = m.Update(key(tea.KeyEsc))
	m = mm.(tokenModel)
	if m.Token() != "" {
		t.Fatalf("Token after esc = %q, want empty", m.Token())
	}
}
