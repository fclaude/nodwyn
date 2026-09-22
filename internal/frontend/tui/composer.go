package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

func newComposer() textarea.Model {
	c := textarea.New()
	c.Prompt = "> "
	c.Placeholder = "Message or :eval Scheme · :help for controls"
	c.ShowLineNumbers = false
	c.CharLimit, c.MaxHeight, c.MaxWidth = 0, 0, 0
	c.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("alt+enter", "ctrl+j"))
	return c
}

// Keep the draft and prompt reply in separate editors. Only the active editor
// receives input; switching to a prompt never copies or rebuilds the draft.
func (m *model) updateEditor(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	if m.prompt != nil {
		m.promptInput, cmd = m.promptInput.Update(msg)
		return cmd
	}
	if paste, ok := msg.(tea.PasteMsg); ok {
		paste.Content = strings.ReplaceAll(paste.Content, "\r\n", "\n")
		paste.Content = strings.ReplaceAll(paste.Content, "\r", "\n")
		msg = paste
	}
	m.composer, cmd = m.composer.Update(msg)
	return cmd
}

func (m *model) layout() {
	m.promptInput.SetWidth(max(1, m.width-3))
	m.composer.SetWidth(m.width)
	m.composer.SetHeight(min(4, max(1, m.height-7)))
	editorHeight := m.composer.Height()
	if m.prompt != nil || m.inspector != nil {
		editorHeight = 1
	}
	m.viewport.SetWidth(m.width)
	m.viewport.SetHeight(max(1, m.height-5-editorHeight))
}
