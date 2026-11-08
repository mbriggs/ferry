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
// On a terminal each line starts with a glyph whose color says how the step
// went, details like shas and paths are dim, long lines wrap under the text
// column, prompts are huh forms, and big transfers show rsync's progress.
// Piped, in tests, or with FERRY_PLAIN=1, it's plain text and line prompts,
// so the output stays easy to grep and script. Plain output doesn't change
// with the styling: tests and scripts read it.

const labelWidth = 10

// textColumn is where a styled line's text starts: a two-space margin, the
// glyph, a space, the label, a space.
const textColumn = 2 + 1 + 1 + labelWidth + 1

var (
	stdoutTTY = term.IsTerminal(int(os.Stdout.Fd()))
	plain     = os.Getenv("FERRY_PLAIN") != "" || os.Getenv("NO_COLOR") != "" || !stdoutTTY

	colorOK   = lipgloss.Color("2")
	colorWarn = lipgloss.Color("3")
	colorBad  = lipgloss.Color("1")
	colorAsk  = lipgloss.Color("6")

	styleFaint = lipgloss.NewStyle().Faint(true)
	styleBold  = lipgloss.NewStyle().Bold(true)
)

// tone is how a step went, which picks its glyph and color on a terminal.
type tone int

const (
	toneOK     tone = iota // it happened
	toneNote               // nothing to act on, like files that stay behind
	toneWarn               // skipped, or needs you next
	toneBad                // failed
	toneAsk                // shows something a prompt is about to ask about
	toneCommit             // the receiver owns the task now
)

var (
	styleWarn = lipgloss.NewStyle().Bold(true).Foreground(colorWarn)
	styleBad  = lipgloss.NewStyle().Bold(true).Foreground(colorBad)
	styleAsk  = lipgloss.NewStyle().Bold(true).Foreground(colorAsk)
	styleOwns = lipgloss.NewStyle().Bold(true).Foreground(colorOK)
)

// tones gives each tone its glyph, the glyph's style, and the label's style.
// Labels of steps that need nothing from you are faint, so the ones that do
// stand out.
var tones = map[tone]struct {
	glyph string
	style lipgloss.Style
	label lipgloss.Style
}{
	toneOK:     {"✓", lipgloss.NewStyle().Foreground(colorOK), styleFaint},
	toneNote:   {"·", styleFaint, styleFaint},
	toneWarn:   {"!", styleWarn, styleWarn},
	toneBad:    {"✗", styleBad, styleBad},
	toneAsk:    {"?", styleAsk, styleFaint},
	toneCommit: {"◆", styleOwns, styleOwns},
}

// interactive reports whether there's someone at a terminal to run an agent
// for: stdin and stdout are both a terminal.
func interactive() bool {
	return stdoutTTY && term.IsTerminal(int(os.Stdin.Fd()))
}

// step prints one line of a handoff that went as planned.
func step(label, format string, a ...any) { say(toneOK, label, format, a...) }

// note prints a step there's nothing to do about.
func note(label, format string, a ...any) { say(toneNote, label, format, a...) }

// warn prints a step that was skipped or that leaves something for you.
func warn(label, format string, a ...any) { say(toneWarn, label, format, a...) }

// bad prints a step that failed.
func bad(label, format string, a ...any) { say(toneBad, label, format, a...) }

// review prints what the next prompt asks about.
func review(label, format string, a ...any) { say(toneAsk, label, format, a...) }

// commitPoint prints the step where the receiver takes ownership. On a
// terminal a rule runs to the edge, since nothing before it is undone after.
func commitPoint(label, format string, a ...any) { say(toneCommit, label, format, a...) }

func say(t tone, label, format string, a ...any) {
	text := fmt.Sprintf(format, a...)
	if plain {
		fmt.Printf("%-*s %s\n", labelWidth, label, text)
		return
	}
	look := tones[t]
	lbl := look.label.Render(fmt.Sprintf("%-*s", labelWidth, label))
	if t == toneCommit {
		text = styleBold.Render(text)
		if width := min(termWidth(), 100); width > 0 {
			if n := width - textColumn - ansi.StringWidth(text) - 1; n > 2 {
				text += " " + styleFaint.Render(strings.Repeat("─", n))
			}
		}
	}
	fmt.Println("  " + look.style.Render(look.glyph) + " " + lbl + " " + wrapHanging(text))
}

// header names the task at the top of a handoff. Plain, it's the task step.
func header(t task, from, to string) {
	if plain {
		step("task", "%s  (%s, repo %s)", t.Name, t.WtRel, t.RepoRel)
		return
	}
	arrow := lipgloss.NewStyle().Foreground(colorAsk).Render("→")
	fmt.Printf("%s  %s  %s %s %s  %s\n", styleFaint.Render("ferry"), styleBold.Render(t.Name), from, arrow, to,
		dim(fmt.Sprintf("%s, repo %s", t.WtRel, t.RepoRel)))
}

// dim renders a detail most runs don't need, like a sha or a generation,
// so it fades on a terminal. Plain, it's s. The result is for printing
// only: on a terminal it holds escape codes, so keep it out of errors,
// records, and argv.
func dim(s string) string {
	if plain {
		return s
	}
	return styleFaint.Render(s)
}

// gutter indents a block of text a step shows, like a hook's source. On a
// terminal a dim bar runs down its left edge.
func gutter(s string) string {
	if plain {
		return indent(s)
	}
	bar := styleFaint.Render("│") + " "
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = bar + l
	}
	return strings.Join(lines, "\n")
}

func termWidth() int {
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return 0
	}
	return width
}

// wrapHanging wraps text to the terminal, indenting continuation lines to
// the text column. Lines the text already breaks (a hook's source) keep
// their breaks.
func wrapHanging(text string) string {
	width := termWidth()
	if width < textColumn+30 {
		return text
	}
	pad := strings.Repeat(" ", textColumn)
	var out []string
	for i, line := range strings.Split(text, "\n") {
		wrapped := strings.Split(ansi.Wrap(line, width-textColumn, " /,"), "\n")
		for j, w := range wrapped {
			if i > 0 || j > 0 {
				w = pad + w
			}
			out = append(out, w)
		}
	}
	return strings.Join(out, "\n")
}

// formTheme draws prompts in the step layout: a ? glyph where a step's glyph
// goes, the question at the text column, and compact buttons beside it.
func formTheme() *huh.Theme {
	t := huh.ThemeBase()
	button := lipgloss.NewStyle().Padding(0, 1).MarginRight(1)
	for _, f := range []*huh.FieldStyles{&t.Focused, &t.Blurred} {
		f.Base = lipgloss.NewStyle()
		f.Card = f.Base
		f.Title = lipgloss.NewStyle().Bold(true).MarginRight(2)
		f.Description = styleFaint.MarginRight(2)
		f.FocusedButton = button.Bold(true).Foreground(lipgloss.Color("0")).Background(colorAsk)
		f.BlurredButton = button.Faint(true)
		f.SelectSelector = lipgloss.NewStyle().Foreground(colorAsk).SetString(strings.Repeat(" ", textColumn-2) + "› ")
		f.Option = lipgloss.NewStyle()
		f.SelectedOption = lipgloss.NewStyle().Bold(true)
	}
	t.Blurred.SelectSelector = lipgloss.NewStyle().SetString(strings.Repeat(" ", textColumn))
	return t
}

// promptLine puts a question at the text column, after a glyph where a
// step's glyph goes.
func promptLine(glyph, q string) string {
	return "  " + glyph + strings.Repeat(" ", textColumn-3) + q
}

// promptTitle puts a question in the step layout, after a ? glyph.
func promptTitle(q string) string { return promptLine(styleAsk.Render("?"), q) }

// answered leaves a one-line record of a prompt and its answer, in place of
// the form, which clears itself.
func answered(q, answer string, ok bool) {
	a := styleFaint.Render(answer)
	if ok {
		a = lipgloss.NewStyle().Foreground(colorOK).Render(answer)
	}
	fmt.Println(promptLine(styleFaint.Render("?"), styleFaint.Render(q)) + "  " + a)
}

// openTTY opens the controlling terminal for a prompt, so prompts work even
// with stdout redirected.
func openTTY() (*os.File, error) {
	return os.OpenFile("/dev/tty", os.O_RDWR, 0)
}

// confirm asks a yes/no question, defaulting to no. With no terminal the
// answer is no. A single y or n answers it.
func confirm(prompt string) bool { return askYesNo(prompt, nil) }

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
	return askYesNo(prompt, km)
}

func askYesNo(prompt string, km *huh.KeyMap) bool {
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
	yes := false
	field := huh.NewConfirm().Title(promptTitle(prompt)).Affirmative("Yes").Negative("No").Inline(true).WithButtonAlignment(lipgloss.Left).Value(&yes)
	if km != nil {
		field = field.Description("←/→ then enter")
	}
	form := huh.NewForm(huh.NewGroup(field)).WithInput(tty).WithOutput(tty).WithShowHelp(false).WithTheme(formTheme())
	if km != nil {
		form = form.WithKeyMap(km)
	}
	ok := form.Run() == nil && yes
	answer := "no"
	if ok {
		answer = "yes"
	}
	answered(prompt, answer, ok)
	return ok
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
		huh.NewSelect[int]().Title(promptTitle(title)).Options(opts...).Value(&choice),
	)).WithInput(tty).WithOutput(tty).WithShowHelp(false).WithTheme(formTheme()).Run()
	if err != nil {
		answered(title, "nothing picked", false)
		return 0, errors.New("nothing picked")
	}
	answered(title, lines[choice], true)
	return choice, nil
}
