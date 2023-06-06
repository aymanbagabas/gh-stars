package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cli/go-gh"
	"github.com/cli/go-gh/pkg/api"
	"github.com/dustin/go-humanize"
	"github.com/guptarohit/asciigraph"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
)

var (
	debug = pflag.BoolP("debug", "d", false, "enable debug output")
)

const (
	perPage        = 100
	reposPath      = "repos/%s"
	stargazersPath = "repos/%s/stargazers"
)

const (
	defaultTimeFormat = "2006-01-02"
)

type view int

const (
	viewGraph view = iota
	viewTable
)

type state int

const (
	stateInit state = iota
	stateReady
	stateError
)

type ErrorMsg error

type Stargazer struct {
	StarredAt time.Time `json:"starred_at"`
}

type StargazersMsg map[string]int

func newStargazersMap(s []Stargazer) StargazersMsg {
	result := make(map[string]int)
	for _, v := range s {
		t := v.StarredAt.Format(defaultTimeFormat)
		result[t]++
	}
	return result
}

func (s StargazersMsg) after(t time.Time) map[string]int {
	result := make(map[string]int)
	for k, v := range s {
		kt, _ := time.Parse(defaultTimeFormat, k)
		if t.Before(kt) {
			result[k] = v
		}
	}
	return result
}

type RepoMsg struct {
	StargazersCount int `json:"stargazers_count"`
}

type Repo struct {
	state      state
	view       view
	width      int
	height     int
	error      error
	name       string
	client     api.RESTClient
	stars      int
	stargazers map[string]int
	spinner    spinner.Model
	table      table.Model
	help       help.Model
	showHelp   bool
	mu         sync.Mutex
	last       int
	all        bool
}

func NewRepo(name string) (*Repo, error) {
	client, err := gh.RESTClient(&api.ClientOptions{
		Headers: map[string]string{
			"Accept": "application/vnd.github.v3.star+json",
		},
	})
	if err != nil {
		return nil, err
	}
	s := spinner.New(spinner.WithSpinner(spinner.Dot))
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	t := table.New(
		table.WithColumns(
			[]table.Column{
				{Title: "Date", Width: 20},
				{Title: "Stars", Width: 10},
			},
		),
		table.WithFocused(true),
	)
	h := help.New()
	h.ShowAll = true
	return &Repo{
		name:    name,
		client:  client,
		spinner: s,
		table:   t,
		help:    h,
		last:    30, // default to 30 days
	}, nil
}

func (r *Repo) TotalStargazerPages() int {
	return r.stars / perPage
}

func (r *Repo) GetStargazers() ([]Stargazer, error) {
	pages := r.TotalStargazerPages()
	if pages >= 400 {
		return nil, fmt.Errorf("Too many pages to fetch")
	}
	var errg errgroup.Group
	stargazers := make([]Stargazer, 0)
	for page := 1; page <= pages; page++ {
		errg.Go(func(page int) func() error {
			return func() error {
				path := fmt.Sprintf(stargazersPath+"?page=%d&per_page=%d", r.name, page, perPage)
				result := make([]Stargazer, 0)
				err := r.client.Get(path, &result)
				if err != nil {
					return fmt.Errorf("Error fetching stargazers page %d: %w", page, err)
				}
				r.mu.Lock()
				stargazers = append(stargazers, result...)
				r.mu.Unlock()
				return nil
			}
		}(page))
	}
	if err := errg.Wait(); err != nil {
		return stargazers, err
	}
	sort.Slice(stargazers, func(i, j int) bool {
		return stargazers[i].StarredAt.Before(stargazers[j].StarredAt)
	})
	return stargazers, nil
}

func (r *Repo) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "toggle all"),
		),
		key.NewBinding(
			key.WithKeys("h", "left"),
			key.WithHelp("left", "before"),
		),
		key.NewBinding(
			key.WithKeys("l", "right"),
			key.WithHelp("right", "after"),
		),
		key.NewBinding(
			key.WithKeys("tab", "shift+tab"),
			key.WithHelp("tab", "section"),
		),
		key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "toggle help"),
		),
		key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}

func (r *Repo) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		r.ShortHelp(),
		{
			r.table.KeyMap.LineUp,
			r.table.KeyMap.LineDown,
			r.table.KeyMap.PageUp,
			r.table.KeyMap.PageDown,
			r.table.KeyMap.HalfPageUp,
			r.table.KeyMap.HalfPageDown,
			r.table.KeyMap.GotoTop,
			r.table.KeyMap.GotoBottom,
		},
	}
}

func (r *Repo) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg {
		repoMsg := RepoMsg{}
		err := r.client.Get(fmt.Sprintf(reposPath, r.name), &repoMsg)
		if err != nil {
			return ErrorMsg(err)
		}
		return repoMsg
	},
		r.spinner.Tick,
	)
}

func (r *Repo) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmds := make([]tea.Cmd, 0)
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		r.width = msg.Width
		r.height = msg.Height
		r.help.Width = r.width
		r.table.SetWidth(r.width)
		r.table.SetHeight(r.height - 1)
	case tea.KeyMsg:
		switch msg.String() {
		case "a":
			r.all = !r.all
		case "h", "left":
			r.last += 30
		case "l", "right":
			if r.last > 30 {
				r.last -= 30
			}
		case "q", "ctrl+c":
			return r, tea.Quit
		case "tab", "shift+tab":
			r.view = (r.view + 1) % 2
		case "?":
			r.showHelp = !r.showHelp
		}
		if r.view == viewTable {
			var cmd tea.Cmd
			r.table, cmd = r.table.Update(msg)
			cmds = append(cmds, cmd)
		}
	case ErrorMsg:
		r.state = stateError
		r.error = msg.(error)
	case spinner.TickMsg:
		var cmd tea.Cmd
		r.spinner, cmd = r.spinner.Update(msg)
		cmds = append(cmds, cmd)
	case StargazersMsg:
		r.stargazers = msg
	case RepoMsg:
		r.stars = msg.StargazersCount
		r.state = stateReady
		cmds = append(cmds, func() tea.Msg {
			stargazers, err := r.GetStargazers()
			if err != nil {
				return ErrorMsg(err)
			}
			return newStargazersMap(stargazers)
		})
	}
	return r, tea.Batch(cmds...)
}

func (r *Repo) View() string {
	if (r.state != stateReady || r.stargazers == nil) && r.state != stateError {
		return fmt.Sprintf("\n %s loading...\n", r.spinner.View())
	}
	if r.state == stateError {
		return fmt.Sprintf("\n Error: %s", r.error)
	}
	if r.showHelp {
		return lipgloss.Place(
			r.width,
			r.height,
			lipgloss.Center,
			lipgloss.Center,
			r.help.View(r),
		)
	}
	keys := make([]string, 0)
	stargazers := r.stargazers
	if r.last > 0 && !r.all {
		stargazers = StargazersMsg(r.stargazers).after(time.Now().AddDate(0, 0, -r.last))
	}
	for k := range stargazers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	switch r.view {
	case viewGraph:
		if len(keys) == 0 {
			return "\n No stargazers found.\n"
		}
		offset := 3
		plot := make([]float64, len(keys))
		for i, k := range keys {
			o := fmt.Sprintf("%d", stargazers[k])
			if len(o) > offset {
				offset = len(o)
			}
			plot[i] = float64(stargazers[k])
		}
		caption := fmt.Sprintf("%s %d stargazers (%s)", r.name, r.stars, humanize.Time(time.Now().AddDate(0, 0, -r.last)))
		if r.all {
			caption = fmt.Sprintf("%s %d stargazers (since %s)", r.name, r.stars, keys[0])
		}
		graph := asciigraph.Plot(
			plot,
			asciigraph.SeriesColors(asciigraph.Blue),
			asciigraph.Width(r.width-offset-1),
			asciigraph.Height(r.height-2),
			asciigraph.Caption(caption),
			asciigraph.Precision(0),
			asciigraph.Offset(offset),
		)
		return graph
	case viewTable:
		rows := make([]table.Row, len(keys))
		for i, j := len(keys)-1, 0; i >= 0; i, j = i-1, j+1 {
			k := keys[i]
			rows[j] = table.Row{k, fmt.Sprintf("%d", r.stargazers[k])}
		}
		r.table.SetRows(rows)
		return r.table.View()
	default:
		return ""
	}
}

func main() {
	var repo string
	pflag.Parse()
	r, err := gh.CurrentRepository()
	if err == nil {
		repo = fmt.Sprintf("%s/%s", r.Owner(), r.Name())
	}
	if len(pflag.Args()) > 0 {
		repo = pflag.Args()[0]
	}
	if repo == "" {
		fmt.Printf("Error: no repository specified\n\n%s\n", "Usage: gh stars [repository]")
		os.Exit(1)
	}
	m, err := NewRepo(repo)
	if err != nil {
		log.Fatalln(err)
	}
	if *debug {
		f, err := tea.LogToFile("debug.txt", "gh-stars")
		if err != nil {
			log.Fatalln(err)
		}
		defer f.Close()
	}
	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
	)
	if err := p.Start(); err != nil {
		log.Fatalln(err)
	}
}
