package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── Стили ──────────────────────────────────────────────────────────────────

var (
	styleTitle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#5C5FD6")).
			Padding(0, 2)

	styleSelected = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FAFAFA")).
			Background(lipgloss.Color("#5C5FD6")).
			PaddingLeft(1)

	styleNormal = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#DDDDDD")).
			PaddingLeft(1)

	styleDir = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7DD6F4")).
			PaddingLeft(1)

	styleSelectedDir = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#7DD6F4")).
				Background(lipgloss.Color("#5C5FD6")).
				PaddingLeft(1)

	styleSearch = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F5A623")).
			Bold(true)

	styleHint = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

	stylePath = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			Italic(true)

	styleResult = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#50FA7B")).
			Bold(true)
)

// ─── Модель ─────────────────────────────────────────────────────────────────

type browserMode int

const (
	modeNormal browserMode = iota
	modeSearch
)

type fileEntry struct {
	name  string
	path  string
	isDir bool
}

type model struct {
	currentDir  string
	entries     []fileEntry
	filtered    []fileEntry
	cursor      int
	mode        browserMode
	searchQuery string
	selected    string
	message     string
	width       int
	height      int
}

func newModel() model {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	m := model{currentDir: cwd}
	m.entries = m.loadDir(cwd)
	m.filtered = m.entries
	return m
}

func (m model) loadDir(dir string) []fileEntry {
	entries := []fileEntry{}

	parent := filepath.Dir(dir)
	if parent != dir {
		entries = append(entries, fileEntry{name: "..", path: parent, isDir: true})
	}

	infos, err := os.ReadDir(dir)
	if err != nil {
		return entries
	}

	for _, info := range infos {
		if info.IsDir() {
			entries = append(entries, fileEntry{
				name:  info.Name() + "/",
				path:  filepath.Join(dir, info.Name()),
				isDir: true,
			})
		}
	}
	for _, info := range infos {
		if !info.IsDir() {
			entries = append(entries, fileEntry{
				name: info.Name(),
				path: filepath.Join(dir, info.Name()),
			})
		}
	}
	return entries
}

func (m model) applyFilter(query string) []fileEntry {
	if query == "" {
		return m.entries
	}
	lower := strings.ToLower(query)
	var result []fileEntry
	for _, e := range m.entries {
		if strings.Contains(strings.ToLower(e.name), lower) {
			result = append(result, e)
		}
	}
	return result
}

// ─── Bubble Tea ──────────────────────────────────────────────────────────────

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tea.KeyMsg:
		switch m.mode {

		case modeNormal:
			switch msg.String() {
			case "ctrl+c", "q":
				return m, tea.Quit
			case "@":
				m.mode = modeSearch
				m.searchQuery = ""
				m.filtered = m.entries
				m.cursor = 0
				m.message = ""
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				}
			case "down", "j":
				if m.cursor < len(m.filtered)-1 {
					m.cursor++
				}
			case "enter":
				if len(m.filtered) == 0 {
					break
				}
				e := m.filtered[m.cursor]
				if e.isDir {
					m.currentDir = e.path
					m.entries = m.loadDir(e.path)
					m.filtered = m.entries
					m.cursor = 0
					m.message = ""
				} else {
					m.selected = e.path
					m.message = e.path
				}
			case "backspace":
				parent := filepath.Dir(m.currentDir)
				if parent != m.currentDir {
					m.currentDir = parent
					m.entries = m.loadDir(parent)
					m.filtered = m.entries
					m.cursor = 0
					m.message = ""
				}
			}

		case modeSearch:
			switch msg.String() {
			case "esc":
				m.mode = modeNormal
				m.searchQuery = ""
				m.filtered = m.entries
				m.cursor = 0
			case "enter":
				if len(m.filtered) > 0 {
					e := m.filtered[m.cursor]
					if e.isDir {
						m.currentDir = e.path
						m.entries = m.loadDir(e.path)
						m.mode = modeNormal
						m.searchQuery = ""
						m.filtered = m.entries
						m.cursor = 0
						m.message = ""
					} else {
						m.selected = e.path
						m.message = e.path
						m.mode = modeNormal
						m.searchQuery = ""
						m.filtered = m.entries
					}
				}
			case "up":
				if m.cursor > 0 {
					m.cursor--
				}
			case "down":
				if m.cursor < len(m.filtered)-1 {
					m.cursor++
				}
			case "backspace":
				if len(m.searchQuery) > 0 {
					m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
					m.filtered = m.applyFilter(m.searchQuery)
					m.cursor = 0
				}
			default:
				if len(msg.String()) == 1 {
					m.searchQuery += msg.String()
					m.filtered = m.applyFilter(m.searchQuery)
					m.cursor = 0
				}
			}
		}
	}
	return m, nil
}

func (m model) View() string {
	if m.width == 0 {
		return "Загрузка..."
	}

	w := m.width
	if w > 80 {
		w = 80
	}
	sep := strings.Repeat("─", w)

	var b strings.Builder

	// Заголовок
	b.WriteString(styleTitle.Render(" File Browser ") + "\n")
	b.WriteString(stylePath.Render("  " + m.currentDir) + "\n")
	b.WriteString(sep + "\n")

	// Строка поиска
	if m.mode == modeSearch {
		b.WriteString(styleSearch.Render("  @ ") + m.searchQuery + "█\n")
		switch len(m.filtered) {
		case 0:
			b.WriteString(styleHint.Render("  ничего не найдено") + "\n")
		default:
			b.WriteString(styleResult.Render(fmt.Sprintf("  найдено: %d", len(m.filtered))) + "\n")
		}
		b.WriteString(sep + "\n")
	}

	// Список файлов
	maxVisible := m.height - 9
	if maxVisible < 4 {
		maxVisible = 4
	}

	start := 0
	if m.cursor >= maxVisible {
		start = m.cursor - maxVisible + 1
	}
	end := start + maxVisible
	if end > len(m.filtered) {
		end = len(m.filtered)
	}

	if len(m.filtered) == 0 {
		b.WriteString(styleHint.Render("  (пусто)") + "\n")
	}

	for i := start; i < end; i++ {
		e := m.filtered[i]
		active := i == m.cursor
		prefix := "  "
		if active {
			prefix = "▶ "
		}
		label := prefix + e.name
		var line string
		switch {
		case active && e.isDir:
			line = styleSelectedDir.Render(label)
		case active:
			line = styleSelected.Render(label)
		case e.isDir:
			line = styleDir.Render(label)
		default:
			line = styleNormal.Render(label)
		}
		b.WriteString(line + "\n")
	}

	if len(m.filtered) > maxVisible {
		b.WriteString(styleHint.Render(fmt.Sprintf("  [%d/%d]", m.cursor+1, len(m.filtered))) + "\n")
	}

	b.WriteString(sep + "\n")

	// Выбранный файл
	if m.message != "" {
		b.WriteString(styleResult.Render("✔ Выбран: "+m.message) + "\n")
	}

	// Подсказки
	b.WriteString("\n")
	if m.mode == modeSearch {
		b.WriteString(styleHint.Render("  ESC — отмена  •  ↑↓ — навигация  •  Enter — выбрать") + "\n")
	} else {
		b.WriteString(styleHint.Render("  @ — поиск  •  ↑↓/jk — навигация  •  Enter — открыть  •  ← — наверх  •  q — выход") + "\n")
	}

	return b.String()
}

// ─── main ────────────────────────────────────────────────────────────────────

func main() {
	p := tea.NewProgram(newModel(), tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
	if m, ok := final.(model); ok && m.selected != "" {
		fmt.Println(m.selected)
	}
}
