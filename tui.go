package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// ── Экраны ───────────────────────────────────────────────────────────────────

type screen int

const (
	screenConnect  screen = iota
	screenRemote
	screenLocal
	screenTransfer
	screenDone
)

// ── Сообщения ─────────────────────────────────────────────────────────────────

type connectedMsg struct {
	sshC  *ssh.Client
	sftpC *sftp.Client
}
type connErrMsg struct{ err error }

type remoteListMsg struct {
	dir     string
	entries []remoteEntry
}
type remoteListErrMsg struct{ err error }

type transferDoneMsg struct{ ok, fail int32 }

// tickMsg — единый тик для всех анимаций (спиннер, прогресс-бар)
type tickMsg struct{}

// ── Типы записей ──────────────────────────────────────────────────────────────

type remoteEntry struct {
	name  string
	path  string
	isDir bool
	size  int64
}

type localEntry struct {
	name  string
	path  string
	isDir bool
}

// progressState — хранится по указателю, чтобы пережить копирование модели
type progressState struct {
	ok   int32
	fail int32
}

// ── Анимация: спиннер ────────────────────────────────────────────────────────

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// ── Цветовая палитра ──────────────────────────────────────────────────────────

var (
	clrPrimary = lipgloss.Color("#5C5FD6")
	clrSuccess = lipgloss.Color("#50FA7B")
	clrWarn    = lipgloss.Color("#F5A623")
	clrError   = lipgloss.Color("#FF5555")
	clrDir     = lipgloss.Color("#7DD6F4")
	clrSubtle  = lipgloss.Color("#555555")
	clrText    = lipgloss.Color("#CCCCCC")
	clrBg      = lipgloss.Color("#1A1A2E")

	stTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Background(clrPrimary).Padding(0, 2)
	stSelected = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Background(clrPrimary)
	stSelDir  = lipgloss.NewStyle().Bold(true).Foreground(clrDir).Background(clrPrimary)
	stNormal  = lipgloss.NewStyle().Foreground(clrText)
	stDir     = lipgloss.NewStyle().Foreground(clrDir)
	stSubtle  = lipgloss.NewStyle().Foreground(clrSubtle)
	stSuccess = lipgloss.NewStyle().Bold(true).Foreground(clrSuccess)
	stWarn    = lipgloss.NewStyle().Foreground(clrWarn)
	stErr     = lipgloss.NewStyle().Foreground(clrError)
	stPath    = lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")).Italic(true)
	stCheck   = lipgloss.NewStyle().Foreground(clrSuccess)
	stSearch  = lipgloss.NewStyle().Bold(true).Foreground(clrWarn)
	stLabel   = lipgloss.NewStyle().Foreground(clrSubtle)
	stLabelOn = lipgloss.NewStyle().Foreground(clrText)

	// Кнопка «Подключиться»
	stBtn = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FAFAFA")).Background(clrPrimary).Padding(0, 3)

	// Поле формы — активное (пурпурный фон, однострочное!)
	stFieldOn = lipgloss.NewStyle().Foreground(lipgloss.Color("#FAFAFA")).Background(clrPrimary).Padding(0, 1)
	// Поле формы — неактивное (тёмный фон, однострочное!)
	stFieldOff = lipgloss.NewStyle().Foreground(clrText).Background(clrBg).Padding(0, 1)
)

// ── Модель ────────────────────────────────────────────────────────────────────

type appModel struct {
	screen     screen
	width      int
	height     int
	err        string
	spinnerIdx int // счётчик кадров спиннера

	// Форма подключения: [host, port, user, password]
	form       [4]string
	formFocus  int
	connecting bool

	// Клиенты
	sshClient  *ssh.Client
	sftpClient *sftp.Client

	// Удалённый браузер
	remoteDir       string
	remoteAll       []remoteEntry
	remoteFiltered  []remoteEntry
	remoteCursor    int
	remoteSearch    string
	remoteSearching bool
	remoteLoading   bool
	selected        map[string]struct{}

	// Локальный пикер
	localDir       string
	localAll       []localEntry
	localFiltered  []localEntry
	localCursor    int
	localSearch    string
	localSearching bool
	destDir        string

	// Передача
	transferTotal int
	progress      *progressState

	// Результат
	doneOK   int32
	doneFail int32
}

func newAppModel() appModel {
	cwd, _ := os.Getwd()
	m := appModel{
		screen:   screenConnect,
		form:     [4]string{"", "22", "", ""},
		selected: make(map[string]struct{}),
		localDir: cwd,
	}
	m.localAll = loadLocalDir(cwd)
	m.localFiltered = m.localAll
	return m
}

// ── Вспомогательные функции ───────────────────────────────────────────────────

func loadLocalDir(dir string) []localEntry {
	var entries []localEntry
	parent := filepath.Dir(dir)
	if parent != dir {
		entries = append(entries, localEntry{name: "..", path: parent, isDir: true})
	}
	infos, err := os.ReadDir(dir)
	if err != nil {
		return entries
	}
	for _, info := range infos {
		if info.IsDir() {
			entries = append(entries, localEntry{
				name:  info.Name() + "/",
				path:  filepath.Join(dir, info.Name()),
				isDir: true,
			})
		}
	}
	for _, info := range infos {
		if !info.IsDir() {
			entries = append(entries, localEntry{
				name: info.Name(),
				path: filepath.Join(dir, info.Name()),
			})
		}
	}
	return entries
}

func filterLocal(entries []localEntry, q string) []localEntry {
	if q == "" {
		return entries
	}
	lower := strings.ToLower(q)
	var out []localEntry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.name), lower) {
			out = append(out, e)
		}
	}
	return out
}

func filterRemote(entries []remoteEntry, q string) []remoteEntry {
	if q == "" {
		return entries
	}
	lower := strings.ToLower(q)
	var out []remoteEntry
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.name), lower) {
			out = append(out, e)
		}
	}
	return out
}

func remoteParent(path string) string {
	p := strings.TrimRight(path, "/")
	if p == "" {
		return "/"
	}
	idx := strings.LastIndex(p, "/")
	if idx <= 0 {
		return "/"
	}
	return p[:idx]
}

func remoteJoin(dir, name string) string {
	return strings.TrimRight(dir, "/") + "/" + name
}

func fmtSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// truncStr обрезает строку до maxRunes символов (Unicode-безопасно)
func truncStr(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

// pad дополняет строку пробелами до нужной ширины (по рунам)
func pad(s string, width int) string {
	r := []rune(s)
	if len(r) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(r))
}

// indent вычисляет отступ для центрирования блока шириной contentW
func indent(contentW, termW int) string {
	off := (termW - contentW) / 2
	if off <= 0 {
		return ""
	}
	return strings.Repeat(" ", off)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func sepLine(w int) string {
	return stSubtle.Render(strings.Repeat("─", clamp(w, 1, 200)))
}

// ── Команды (async) ───────────────────────────────────────────────────────────

func cmdConnect(host, port, user, pass string) tea.Cmd {
	return func() tea.Msg {
		cfg := &ssh.ClientConfig{
			User:            user,
			Auth:            []ssh.AuthMethod{ssh.Password(pass)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         10 * time.Second,
		}
		sc, err := ssh.Dial("tcp", host+":"+port, cfg)
		if err != nil {
			return connErrMsg{err}
		}
		fc, err := sftp.NewClient(sc)
		if err != nil {
			sc.Close()
			return connErrMsg{err}
		}
		return connectedMsg{sshC: sc, sftpC: fc}
	}
}

func cmdListRemote(client *sftp.Client, dir string) tea.Cmd {
	return func() tea.Msg {
		target := dir
		if target == "" {
			if wd, err := client.Getwd(); err == nil {
				target = wd
			} else {
				target = "/"
			}
		}
		infos, err := client.ReadDir(target)
		if err != nil {
			return remoteListErrMsg{err}
		}
		var entries []remoteEntry
		parent := remoteParent(target)
		if parent != target {
			entries = append(entries, remoteEntry{name: "..", path: parent, isDir: true})
		}
		for _, info := range infos {
			if info.IsDir() {
				entries = append(entries, remoteEntry{
					name:  info.Name() + "/",
					path:  remoteJoin(target, info.Name()),
					isDir: true,
				})
			}
		}
		for _, info := range infos {
			if !info.IsDir() {
				entries = append(entries, remoteEntry{
					name: info.Name(),
					path: remoteJoin(target, info.Name()),
					size: info.Size(),
				})
			}
		}
		return remoteListMsg{dir: target, entries: entries}
	}
}

func cmdTransfer(
	cfg PipelineCfg,
	sftpClient *sftp.Client,
	jobs []FileJob,
	destDir string,
	progress *progressState,
) tea.Cmd {
	return func() tea.Msg {
		processFunc := func(result FileResult) error {
			dest := filepath.Join(destDir, filepath.Base(result.ID))
			if err := os.WriteFile(dest, result.Data, 0644); err != nil {
				atomic.AddInt32(&progress.fail, 1)
				return err
			}
			atomic.AddInt32(&progress.ok, 1)
			return nil
		}
		_, fail := cfg.TransferFiles(sftpClient, jobs, processFunc)
		ok := atomic.LoadInt32(&progress.ok)
		return transferDoneMsg{ok: ok, fail: fail}
	}
}

// cmdTick — непрерывный тик 120мс для всех анимаций
func cmdTick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

// ── Bubble Tea: Init / Update ─────────────────────────────────────────────────

// Init запускает непрерывный тик сразу при старте
func (m appModel) Init() tea.Cmd {
	return cmdTick()
}

func (m appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	// Тик: всегда продолжаем + увеличиваем счётчик спиннера
	case tickMsg:
		m.spinnerIdx++
		return m, cmdTick()

	case connectedMsg:
		m.connecting = false
		m.sshClient = msg.sshC
		m.sftpClient = msg.sftpC
		m.screen = screenRemote
		m.remoteLoading = true
		m.err = ""
		return m, cmdListRemote(m.sftpClient, "")

	case connErrMsg:
		m.connecting = false
		m.err = msg.err.Error()
		return m, nil

	case remoteListMsg:
		m.remoteLoading = false
		m.remoteDir = msg.dir
		m.remoteAll = msg.entries
		m.remoteFiltered = filterRemote(msg.entries, m.remoteSearch)
		m.remoteCursor = 0
		m.err = ""
		return m, nil

	case remoteListErrMsg:
		m.remoteLoading = false
		m.err = msg.err.Error()
		return m, nil

	case transferDoneMsg:
		m.doneOK = msg.ok
		m.doneFail = msg.fail
		m.screen = screenDone
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m appModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.screen {
	case screenConnect:
		return m.handleConnect(msg)
	case screenRemote:
		return m.handleRemote(msg)
	case screenLocal:
		return m.handleLocal(msg)
	case screenDone:
		if msg.String() == "enter" || msg.String() == "q" {
			return m, tea.Quit
		}
	}
	return m, nil
}

// ── Обработчик: форма подключения ─────────────────────────────────────────────

func (m appModel) handleConnect(msg tea.KeyMsg) (appModel, tea.Cmd) {
	if m.connecting {
		return m, nil
	}
	switch msg.String() {
	case "tab", "down":
		m.err = ""
		m.formFocus = (m.formFocus + 1) % 4
	case "up", "shift+tab":
		m.err = ""
		m.formFocus = (m.formFocus + 3) % 4
	case "enter":
		if m.formFocus < 3 {
			m.formFocus++
		} else {
			return m.doConnect()
		}
	case "backspace":
		f := m.form[m.formFocus]
		if len(f) > 0 {
			m.form[m.formFocus] = f[:len(f)-1]
		}
	default:
		if len(msg.String()) == 1 {
			m.form[m.formFocus] += msg.String()
		}
	}
	return m, nil
}

func (m appModel) doConnect() (appModel, tea.Cmd) {
	host := strings.TrimSpace(m.form[0])
	port := strings.TrimSpace(m.form[1])
	user := strings.TrimSpace(m.form[2])
	pass := m.form[3]
	if host == "" {
		m.err = "Укажите хост"
		return m, nil
	}
	if user == "" {
		m.err = "Укажите пользователя"
		return m, nil
	}
	if port == "" {
		port = "22"
		m.form[1] = "22"
	}
	m.connecting = true
	m.err = ""
	return m, cmdConnect(host, port, user, pass)
}

// ── Обработчик: удалённый браузер ─────────────────────────────────────────────

func (m appModel) handleRemote(msg tea.KeyMsg) (appModel, tea.Cmd) {
	if m.remoteLoading {
		return m, nil
	}
	if m.remoteSearching {
		switch msg.String() {
		case "esc":
			m.remoteSearching = false
			m.remoteSearch = ""
			m.remoteFiltered = m.remoteAll
			m.remoteCursor = 0
		case "backspace":
			if len(m.remoteSearch) > 0 {
				m.remoteSearch = m.remoteSearch[:len(m.remoteSearch)-1]
				m.remoteFiltered = filterRemote(m.remoteAll, m.remoteSearch)
				m.remoteCursor = 0
			}
		case "enter":
			m.remoteSearching = false
		case "up":
			if m.remoteCursor > 0 {
				m.remoteCursor--
			}
		case "down":
			if m.remoteCursor < len(m.remoteFiltered)-1 {
				m.remoteCursor++
			}
		default:
			if len(msg.String()) == 1 {
				m.remoteSearch += msg.String()
				m.remoteFiltered = filterRemote(m.remoteAll, m.remoteSearch)
				m.remoteCursor = 0
			}
		}
		return m, nil
	}
	switch msg.String() {
	case "@":
		m.remoteSearching = true
		m.remoteSearch = ""
	case "up", "k":
		if m.remoteCursor > 0 {
			m.remoteCursor--
		}
	case "down", "j":
		if m.remoteCursor < len(m.remoteFiltered)-1 {
			m.remoteCursor++
		}
	case "enter":
		if e := m.remoteCurrent(); e != nil && e.isDir {
			m.remoteLoading = true
			return m, cmdListRemote(m.sftpClient, e.path)
		}
	case " ":
		if e := m.remoteCurrent(); e != nil && !e.isDir {
			if _, ok := m.selected[e.path]; ok {
				delete(m.selected, e.path)
			} else {
				m.selected[e.path] = struct{}{}
			}
		}
	case "backspace":
		parent := remoteParent(m.remoteDir)
		if parent != m.remoteDir {
			m.remoteLoading = true
			return m, cmdListRemote(m.sftpClient, parent)
		}
	case "c":
		if len(m.selected) == 0 {
			m.err = "Выберите файлы клавишей Space"
			return m, nil
		}
		m.err = ""
		m.screen = screenLocal
		m.localAll = loadLocalDir(m.localDir)
		m.localFiltered = filterLocal(m.localAll, m.localSearch)
	}
	return m, nil
}

func (m *appModel) remoteCurrent() *remoteEntry {
	if len(m.remoteFiltered) == 0 || m.remoteCursor >= len(m.remoteFiltered) {
		return nil
	}
	e := m.remoteFiltered[m.remoteCursor]
	return &e
}

// ── Обработчик: локальный пикер ───────────────────────────────────────────────

func (m appModel) handleLocal(msg tea.KeyMsg) (appModel, tea.Cmd) {
	if m.localSearching {
		switch msg.String() {
		case "esc":
			m.localSearching = false
			m.localSearch = ""
			m.localFiltered = m.localAll
			m.localCursor = 0
		case "backspace":
			if len(m.localSearch) > 0 {
				m.localSearch = m.localSearch[:len(m.localSearch)-1]
				m.localFiltered = filterLocal(m.localAll, m.localSearch)
				m.localCursor = 0
			}
		case "enter":
			m.localSearching = false
		case "up":
			if m.localCursor > 0 {
				m.localCursor--
			}
		case "down":
			if m.localCursor < len(m.localFiltered)-1 {
				m.localCursor++
			}
		default:
			if len(msg.String()) == 1 {
				m.localSearch += msg.String()
				m.localFiltered = filterLocal(m.localAll, m.localSearch)
				m.localCursor = 0
			}
		}
		return m, nil
	}
	switch msg.String() {
	case "@":
		m.localSearching = true
		m.localSearch = ""
	case "up", "k":
		if m.localCursor > 0 {
			m.localCursor--
		}
	case "down", "j":
		if m.localCursor < len(m.localFiltered)-1 {
			m.localCursor++
		}
	case "enter":
		if len(m.localFiltered) > 0 {
			e := m.localFiltered[m.localCursor]
			if e.isDir {
				m.localDir = e.path
				m.localAll = loadLocalDir(e.path)
				m.localFiltered = filterLocal(m.localAll, m.localSearch)
				m.localCursor = 0
			}
		}
	case "backspace":
		parent := filepath.Dir(m.localDir)
		if parent != m.localDir {
			m.localDir = parent
			m.localAll = loadLocalDir(parent)
			m.localFiltered = filterLocal(m.localAll, m.localSearch)
			m.localCursor = 0
		}
	case "c":
		m.destDir = m.localDir
		m.screen = screenTransfer
		m.progress = &progressState{}

		var jobs []FileJob
		for path := range m.selected {
			jobs = append(jobs, FileJob{RemotePath: path, ID: path})
		}
		m.transferTotal = len(jobs)
		return m, cmdTransfer(DefaultCfg(), m.sftpClient, jobs, m.destDir, m.progress)
	}
	return m, nil
}

// ── View: маршрутизатор ───────────────────────────────────────────────────────

const minW, minH = 48, 12

func (m appModel) View() string {
	if m.width == 0 {
		return ""
	}
	// Минимальный размер терминала
	if m.width < minW || m.height < minH {
		return fmt.Sprintf(
			"\n\n  %s\n  %s\n\n  Текущий:  %d × %d\n  Минимум:  %d × %d",
			stErr.Render("Терминал слишком мал"),
			stSubtle.Render("Увеличьте окно терминала"),
			m.width, m.height, minW, minH,
		)
	}
	switch m.screen {
	case screenConnect:
		return m.viewConnect()
	case screenRemote:
		return m.viewRemote()
	case screenLocal:
		return m.viewLocal()
	case screenTransfer:
		return m.viewTransfer()
	case screenDone:
		return m.viewDone()
	}
	return ""
}

// ── Экран 1: подключение ──────────────────────────────────────────────────────
//
// Адаптивность:
//   - formW = min(58, width-4)  — форма не шире терминала
//   - fieldW = formW - 20       — ширина поля ввода
//   - На широком экране — форма центрируется
//
// Анимация:
//   - Спиннер крутится пока connecting == true

func (m appModel) viewConnect() string {
	// Адаптивные размеры
	formW := clamp(m.width-4, 36, 60)
	fieldW := clamp(formW-20, 12, 38)

	// Отступ для центрирования на широком экране
	off := indent(formW+4, m.width)

	labels := [4]string{"Хост", "Порт", "Пользователь", "Пароль"}
	var sb strings.Builder

	// Заголовок
	titleText := stTitle.Render(" 🔗 SSH File Transfer ")
	titleOff := indent(lipgloss.Width(titleText), formW+4)
	sb.WriteString(off + titleOff + titleText + "\n\n")

	// Поля формы — все однострочные (фон вместо рамки)
	for i, label := range labels {
		val := m.form[i]
		display := val
		if i == 3 {
			display = strings.Repeat("●", len([]rune(val)))
		}

		// Курсор мигает: чётные тики — показываем, нечётные — пробел
		cursor := " "
		if i == m.formFocus {
			if (m.spinnerIdx/4)%2 == 0 {
				cursor = "█"
			}
		}

		// Обрезаем + дополняем до нужной ширины
		content := display + cursor
		runes := []rune(content)
		if len(runes) > fieldW {
			runes = runes[len(runes)-fieldW:]
		}
		padded := pad(string(runes), fieldW)

		var fieldStr string
		var labelSt lipgloss.Style
		if i == m.formFocus {
			fieldStr = stFieldOn.Render(padded)
			labelSt = stLabelOn
		} else {
			fieldStr = stFieldOff.Render(padded)
			labelSt = stLabel
		}

		row := off + "  " + labelSt.Render(fmt.Sprintf("%-14s", label+":")) + " " + fieldStr
		sb.WriteString(row + "\n")
	}

	// Кнопка / спиннер
	sb.WriteString("\n" + off + "  ")
	if m.connecting {
		frame := spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
		sb.WriteString(stWarn.Render(frame + " Подключение..."))
	} else {
		sb.WriteString(stBtn.Render("  Подключиться  "))
	}
	sb.WriteString("\n")

	if m.err != "" {
		sb.WriteString("\n" + off + "  " + stErr.Render("✖ "+m.err) + "\n")
	}

	sb.WriteString("\n" + stSubtle.Render("  Tab/↑↓ — поле  •  Enter — след./подключиться  •  Ctrl+C — выход"))
	return sb.String()
}

// ── Экран 2: удалённый браузер ────────────────────────────────────────────────
//
// Адаптивность:
//   - listW = min(82, width)      — ширина списка
//   - maxV  = height - заголовок  — высота списка
//   - Имя файла обрезается до (listW - 14) символов
//   - Размер файла — правая колонка, только если listW > 50

func (m appModel) viewRemote() string {
	listW := clamp(m.width, minW, 90)
	sep := sepLine(listW)
	var sb strings.Builder

	// Шапка
	dirStr := truncStr(m.remoteDir, listW-20)
	sb.WriteString(stTitle.Render(fmt.Sprintf(" 📡 %s ", dirStr)) + "\n")
	if n := len(m.selected); n > 0 {
		sb.WriteString(stCheck.Render(fmt.Sprintf("  ✔ Выбрано: %d", n)) + "\n")
	} else {
		sb.WriteString(stSubtle.Render("  Space — отметить  •  c — далее") + "\n")
	}
	sb.WriteString(sep + "\n")

	// Строка поиска
	if m.remoteSearching {
		sb.WriteString(stSearch.Render("  @ ") + m.remoteSearch + "█\n")
		if len(m.remoteFiltered) == 0 {
			sb.WriteString(stSubtle.Render("  (ничего не найдено)") + "\n")
		} else {
			sb.WriteString(stWarn.Render(fmt.Sprintf("  найдено: %d", len(m.remoteFiltered))) + "\n")
		}
		sb.WriteString(sep + "\n")
	}

	// Анимированная загрузка
	if m.remoteLoading {
		frame := spinnerFrames[m.spinnerIdx%len(spinnerFrames)]
		sb.WriteString("\n  " + stWarn.Render(frame+" Загрузка...") + "\n")
		return sb.String()
	}

	// Список (высота адаптируется)
	headerLines := 4
	if m.remoteSearching {
		headerLines += 3
	}
	footerLines := 2
	maxV := clamp(m.height-headerLines-footerLines, 3, 100)
	m.renderRemoteList(&sb, listW, maxV)

	sb.WriteString(sep + "\n")
	if m.err != "" {
		sb.WriteString(stErr.Render("  ✖ "+m.err) + "\n")
	}
	if m.remoteSearching {
		sb.WriteString(stSubtle.Render("  ESC — сбросить  •  ↑↓ — навигация  •  Enter — применить"))
	} else {
		sb.WriteString(stSubtle.Render("  @ — поиск  •  ↑↓/jk — навиг.  •  Enter — открыть  •  Space — выбрать  •  ← — назад  •  c — далее"))
	}
	return sb.String()
}

func (m appModel) renderRemoteList(sb *strings.Builder, listW, maxV int) {
	if len(m.remoteFiltered) == 0 {
		sb.WriteString(stSubtle.Render("  (пусто)") + "\n")
		return
	}
	showSize := listW > 52

	start := 0
	if m.remoteCursor >= maxV {
		start = m.remoteCursor - maxV + 1
	}
	end := clamp(start+maxV, 0, len(m.remoteFiltered))

	for i := start; i < end; i++ {
		e := m.remoteFiltered[i]
		active := i == m.remoteCursor
		_, isSel := m.selected[e.path]

		prefix := "  "
		if active {
			prefix = "▶ "
		}
		check := "  "
		if isSel {
			check = stCheck.Render("✔ ")
		}

		// Адаптивная ширина имени
		nameW := listW - 6 // prefix(2) + check(2) + margin(2)
		if showSize {
			nameW -= 8 // резервируем под размер
		}
		if nameW < 4 {
			nameW = 4
		}
		name := pad(truncStr(e.name, nameW), nameW)

		sizeStr := ""
		if showSize && !e.isDir && e.size > 0 {
			sizeStr = stSubtle.Render(fmt.Sprintf(" %6s", fmtSize(e.size)))
		}

		label := prefix + check + name
		var line string
		switch {
		case active && e.isDir:
			line = stSelDir.Render(label) + sizeStr
		case active:
			line = stSelected.Render(label) + sizeStr
		case e.isDir:
			line = stDir.Render(label) + sizeStr
		default:
			line = stNormal.Render(label) + sizeStr
		}
		sb.WriteString(line + "\n")
	}

	if len(m.remoteFiltered) > maxV {
		sb.WriteString(stSubtle.Render(fmt.Sprintf(
			"  ↑↓ [%d/%d]", m.remoteCursor+1, len(m.remoteFiltered),
		)) + "\n")
	}
}

// ── Экран 3: локальный пикер ─────────────────────────────────────────────────

func (m appModel) viewLocal() string {
	listW := clamp(m.width, minW, 90)
	sep := sepLine(listW)
	var sb strings.Builder

	sb.WriteString(stTitle.Render(" 💾 Куда сохранить? ") + "\n")
	sb.WriteString(stPath.Render("  "+truncStr(m.localDir, listW-4)) + "\n")
	sb.WriteString(sep + "\n")

	if m.localSearching {
		sb.WriteString(stSearch.Render("  @ ") + m.localSearch + "█\n")
		if len(m.localFiltered) == 0 {
			sb.WriteString(stSubtle.Render("  (ничего не найдено)") + "\n")
		} else {
			sb.WriteString(stWarn.Render(fmt.Sprintf("  найдено: %d", len(m.localFiltered))) + "\n")
		}
		sb.WriteString(sep + "\n")
	}

	headerLines := 4
	if m.localSearching {
		headerLines += 3
	}
	maxV := clamp(m.height-headerLines-2, 3, 100)
	m.renderLocalList(&sb, listW, maxV)

	sb.WriteString(sep + "\n")
	if m.err != "" {
		sb.WriteString(stErr.Render("  ✖ "+m.err) + "\n")
	}
	if m.localSearching {
		sb.WriteString(stSubtle.Render("  ESC — сбросить  •  ↑↓ — навигация"))
	} else {
		sb.WriteString(stSubtle.Render("  @ — поиск  •  ↑↓/jk — навиг.  •  Enter — открыть  •  ← — назад  •  c — передать сюда"))
	}
	return sb.String()
}

func (m appModel) renderLocalList(sb *strings.Builder, listW, maxV int) {
	if len(m.localFiltered) == 0 {
		sb.WriteString(stSubtle.Render("  (пусто)") + "\n")
		return
	}
	nameW := clamp(listW-4, 4, listW)

	start := 0
	if m.localCursor >= maxV {
		start = m.localCursor - maxV + 1
	}
	end := clamp(start+maxV, 0, len(m.localFiltered))

	for i := start; i < end; i++ {
		e := m.localFiltered[i]
		active := i == m.localCursor
		prefix := "  "
		if active {
			prefix = "▶ "
		}
		name := truncStr(e.name, nameW-2)
		label := prefix + name
		var line string
		switch {
		case active && e.isDir:
			line = stSelDir.Render(label)
		case active:
			line = stSelected.Render(label)
		case e.isDir:
			line = stDir.Render(label)
		default:
			line = stNormal.Render(label)
		}
		sb.WriteString(line + "\n")
	}
	if len(m.localFiltered) > maxV {
		sb.WriteString(stSubtle.Render(fmt.Sprintf(
			"  ↑↓ [%d/%d]", m.localCursor+1, len(m.localFiltered),
		)) + "\n")
	}
}

// ── Экран 4: прогресс передачи ────────────────────────────────────────────────
//
// Анимация:
//   - Спиннер перед заголовком
//   - Прогресс-бар адаптивной ширины

func (m appModel) viewTransfer() string {
	ok := int(atomic.LoadInt32(&m.progress.ok))
	total := m.transferTotal

	barW := clamp(m.width-24, 10, 60)
	pct := 0
	if total > 0 {
		pct = ok * 100 / total
	}

	filled := 0
	if total > 0 {
		filled = ok * barW / total
	}
	bar := stSuccess.Render(strings.Repeat("█", filled)) +
		stSubtle.Render(strings.Repeat("░", barW-filled))

	frame := spinnerFrames[m.spinnerIdx%len(spinnerFrames)]

	var sb strings.Builder
	sb.WriteString(stTitle.Render(" ⚡ Передача файлов ") + "\n\n")
	sb.WriteString(fmt.Sprintf("  %s  %s\n\n",
		stWarn.Render(frame),
		bar,
	))
	sb.WriteString(fmt.Sprintf("  %s  %d%%\n\n",
		stSubtle.Render(fmt.Sprintf("%d / %d файлов", ok, total)),
		pct,
	))
	sb.WriteString(stPath.Render("  → "+m.destDir) + "\n\n")
	sb.WriteString(stSubtle.Render("  Ctrl+C — выход"))
	return sb.String()
}

// ── Экран 5: результат ────────────────────────────────────────────────────────

func (m appModel) viewDone() string {
	var sb strings.Builder
	sb.WriteString(stTitle.Render(" ✅ Готово! ") + "\n\n")
	sb.WriteString(stSuccess.Render(fmt.Sprintf("  Успешно:  %d файл(ов)", m.doneOK)) + "\n")
	if m.doneFail > 0 {
		sb.WriteString(stErr.Render(fmt.Sprintf("  Ошибок:   %d", m.doneFail)) + "\n")
	}
	sb.WriteString("\n" + stPath.Render("  Папка: "+m.destDir) + "\n\n")
	sb.WriteString(stSubtle.Render("  Enter / q — выход"))
	return sb.String()
}

// ── main ──────────────────────────────────────────────────────────────────────

func main() {
	p := tea.NewProgram(newAppModel(), tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
	if m, ok := final.(appModel); ok {
		if m.sftpClient != nil {
			m.sftpClient.Close()
		}
		if m.sshClient != nil {
			m.sshClient.Close()
		}
	}
}
