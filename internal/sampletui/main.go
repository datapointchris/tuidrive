// sampletui is the Bubble Tea v2 program this module's tests drive: a short list
// that holds a blank frame while it loads, opens a row, takes typed text, and shows
// the environment it was given.
package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	yellow  = "\x1b[93m"
	reverse = "\x1b[7m"
	bold    = "\x1b[1m"
	reset   = "\x1b[m"
)

type row struct{ title, state, kind string }

var rows = []row{
	{"first row", "new", "note"},
	{"second row", "done", "task"},
	{"third row", "new", "link"},
}

type view int

const (
	viewLoading view = iota
	viewList
	viewOpen
	viewFilter
	viewEnvironment
)

type loadedMsg struct{}

type model struct {
	current view
	cursor  int
	filter  string
	started time.Time
}

func (m model) Init() tea.Cmd {
	return tea.Tick(300*time.Millisecond, func(time.Time) tea.Msg { return loadedMsg{} })
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case loadedMsg:
		m.current = viewList
	case tea.KeyPressMsg:
		return m.pressed(msg.String())
	}
	return m, nil
}

func (m model) pressed(key string) (tea.Model, tea.Cmd) {
	if m.current == viewFilter {
		switch key {
		case "enter", "esc":
			m.current = viewList
		default:
			m.filter += key
		}
		return m, nil
	}
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.current = viewList
	case "j", "down":
		m.cursor = min(m.cursor+1, len(rows)-1)
	case "k", "up":
		m.cursor = max(m.cursor-1, 0)
	case "enter":
		m.current = viewOpen
	case "e":
		m.current = viewEnvironment
	case "/":
		m.current = viewFilter
	}
	return m, nil
}

func (m model) View() tea.View {
	drawn := tea.NewView(m.render())
	drawn.AltScreen = true
	return drawn
}

func (m model) render() string {
	switch m.current {
	case viewLoading:
		return ""
	case viewOpen:
		chosen := rows[m.cursor]
		return fmt.Sprintf("%s%s%s\n\nkind: %s\nstate: %s\n\nesc back", bold, chosen.title, reset, chosen.kind, chosen.state)
	case viewEnvironment:
		return fmt.Sprintf("stray=%s\nchosen=%s\nstate=%s\n\nesc back",
			os.Getenv("STRAY"), os.Getenv("CHOSEN"), os.Getenv("XDG_STATE_HOME"))
	case viewFilter:
		return "filter: " + m.filter
	case viewList:
		return m.renderList()
	default:
		return fmt.Sprintf("view %d has no renderer", m.current)
	}
}

func (m model) renderList() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%sinbox%s  sort newest", bold, reset)
	if m.filter != "" {
		fmt.Fprintf(&b, "  filter %s", m.filter)
	}
	age := int(time.Since(m.started).Seconds())
	b.WriteString("\n\n")
	for i, r := range rows {
		state := r.state
		if state == "new" {
			state = yellow + state + reset
		}
		title := fmt.Sprintf("%-12s", r.title)
		if i == m.cursor {
			title = reverse + title + reset
		}
		fmt.Fprintf(&b, "  %s %s  %ds ago\n", title, state, age+i)
	}
	b.WriteString("\nj k move  enter open  / filter  e env  q quit")
	return b.String()
}

func main() {
	if _, err := tea.NewProgram(model{started: time.Now()}).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
