package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/term"
)

// Ferry's output is one step per line: a label column and what happened.
// On a terminal the labels are colored, long lines wrap under the text
// column, prompts are huh forms, and big transfers show rsync's progress. Piped,
// in tests, or with FERRY_PLAIN=1, it's plain text and line prompts, so the
// output stays easy to grep and script.

const labelWidth = 10

var (
	stdoutTTY = term.IsTerminal(int(os.Stdout.Fd()))
	plain     = os.Getenv("FERRY_PLAIN") != "" || os.Getenv("NO_COLOR") != "" || !stdoutTTY

	styleLabel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	styleGood  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("2"))
	styleWarn  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	styleBad   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
	styleFaint = lipgloss.NewStyle().Faint(true)
)

// interactive reports whether there's someone at a terminal to run an agent
// for: stdin and stdout are both a terminal.
func interactive() bool {
	return stdoutTTY && term.IsTerminal(int(os.Stdin.Fd()))
}

// step prints one line of a handoff.
func step(label, format string, a ...any) {
	text := fmt.Sprintf(format, a...)
	if plain {
		fmt.Printf("%-*s %s\n", labelWidth, label, text)
		return
	}
	fmt.Println(styleFor(label, text).Render(fmt.Sprintf("%-*s", labelWidth, label)) + " " + wrapHanging(text))
}

func styleFor(label, text string) lipgloss.Style {
	switch {
	case strings.HasPrefix(text, "failed") || label == "fix":
		return styleBad
	case label == "time":
		return styleFaint
	case label == "left" || label == "next" || label == "claude" || strings.HasPrefix(text, "skipped"):
		return styleWarn
	case label == "launch" || label == "resume" || label == "commit" || label == "done":
		return styleGood
	}
	return styleLabel
}

// wrapHanging wraps text to the terminal, indenting continuation lines to
// the text column. Lines the text already breaks (a hook's source) keep
// their breaks.
func wrapHanging(text string) string {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || width < labelWidth+30 {
		return text
	}
	indent := strings.Repeat(" ", labelWidth+1)
	var out []string
	for i, line := range strings.Split(text, "\n") {
		wrapped := strings.Split(ansi.Wrap(line, width-labelWidth-1, " /,"), "\n")
		for j, w := range wrapped {
			if i > 0 || j > 0 {
				w = indent + w
			}
			out = append(out, w)
		}
	}
	return strings.Join(out, "\n")
}

// openTTY opens the controlling terminal for a prompt, so prompts work even
// with stdout redirected.
func openTTY() (*os.File, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
}

// confirm asks a yes/no question, defaulting to no. With no terminal the
// answer is no. A single y or n answers it.
func confirm(prompt string) bool { return ask(prompt, nil) }

// confirmRun asks before running code: a hook, or an agent whose config
// changed. A keypress mustn't approve it by accident (text typed ahead of
// the prompt, as in "ferry", holds a y), so y/n, the h/l toggles and Tab
// are off: Yes takes an arrow key and then Enter, and anything else typed
// can only submit the default, No.
func confirmRun(prompt string) bool {
	km := huh.NewDefaultKeyMap()
	km.Confirm.Accept = key.NewBinding(key.WithDisabled())
	km.Confirm.Reject = key.NewBinding(key.WithDisabled())
	km.Confirm.Toggle = key.NewBinding(key.WithKeys("left", "right"), key.WithHelp("←/→", "toggle"))
	km.Confirm.Next = key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next"))
	return ask(prompt, km)
}

func ask(prompt string, km *huh.KeyMap) bool {
	defer clock.pause()()
	tty, err := openTTY()
	if err != nil {
		return false
	}
	defer tty.Close()
	if plain {
		fmt.Fprintf(tty, "%s [y/N] ", prompt)
		line, _ := bufio.NewReader(tty).ReadString('\n')
		line = strings.ToLower(strings.TrimSpace(line))
		return line == "y" || line == "yes"
	}
	if km != nil {
		prompt += " (←/→ then Enter)"
	}
	yes := false
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(prompt).Affirmative("Yes").Negative("No").Value(&yes),
	)).WithInput(tty).WithOutput(tty).WithShowHelp(false)
	if km != nil {
		form = form.WithKeyMap(km)
	}
	return form.Run() == nil && yes
}

// pick asks the user to choose one of lines.
func pick(title string, lines []string) (int, error) {
	defer clock.pause()()
	tty, err := openTTY()
	if err != nil {
		return 0, errors.New(title + " more than one match, and no terminal to ask on:\n  " + strings.Join(lines, "\n  "))
	}
	defer tty.Close()
	if plain {
		fmt.Fprintln(tty, title)
		for i, l := range lines {
			fmt.Fprintf(tty, "  %d  %s\n", i+1, l)
		}
		r := bufio.NewReader(tty)
		for {
			fmt.Fprintf(tty, "pick 1-%d: ", len(lines))
			line, err := r.ReadString('\n')
			n, convErr := strconv.Atoi(strings.TrimSpace(line))
			if convErr == nil && n >= 1 && n <= len(lines) {
				return n - 1, nil
			}
			if err != nil {
				return 0, errors.New("nothing picked")
			}
		}
	}
	opts := make([]huh.Option[int], len(lines))
	for i, l := range lines {
		opts[i] = huh.NewOption(l, i)
	}
	choice := 0
	err = huh.NewForm(huh.NewGroup(
		huh.NewSelect[int]().Title(title).Options(opts...).Value(&choice),
	)).WithInput(tty).WithOutput(tty).WithShowHelp(false).Run()
	if err != nil {
		return 0, errors.New("nothing picked")
	}
	return choice, nil
}
