package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// schemaVersion is the version of every --json document. It goes up only when a field is
// removed, renamed or changes meaning; new fields can appear without it changing.
const schemaVersion = 1

// Exit codes, the same for every command.
const (
	exitOK       = 0
	exitError    = 1 // the command failed; with --json the error is on stdout
	exitUsage    = 2 // bad flags or arguments
	exitNotFound = 3 // where: no worktree has the branch
)

// jsonMode and command are set once the command line is parsed, so that fatal errors raised
// deep inside a command still come out in the format the caller asked for.
var (
	jsonMode bool
	command  = "scan"
)

// Reason is why something happened or was decided: a stable snake_case code for scripts and agents,
// and a sentence for people. The codes are listed in AGENTS.md.
type Reason struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

// header starts every --json document.
type header struct {
	SchemaVersion int    `json:"schemaVersion"`
	Command       string `json:"command"`
	OK            bool   `json:"ok"`
}

func okHeader() header { return header{schemaVersion, command, true} }

// nextStep is a command the caller can run next, as an agent would type it.
type nextStep struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// cliError is a failure with a stable code, so --json callers can tell failures apart without parsing text.
type cliError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *cliError) Error() string { return e.Message }

func failure(code string, err error) *cliError {
	var ce *cliError
	if errors.As(err, &ce) {
		return ce
	}
	return &cliError{code, err.Error()}
}

// fatal reports err and exits. With --json the error goes to stdout as a document of its own,
// so a caller reading stdout always gets JSON.
func fatal(err error) {
	exitWith(exitError, failure("error", err))
}

func exitWith(code int, e *cliError) {
	if jsonMode {
		printJSON(struct {
			header
			Error *cliError `json:"error"`
		}{header{schemaVersion, command, false}, e})
	} else {
		fmt.Fprintln(os.Stderr, errStyle.red("git-mop: ")+e.Message)
	}
	os.Exit(code)
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(os.Stderr, "git-mop:", err)
		os.Exit(exitError)
	}
}

// progress prints a status line for a person waiting at a terminal, and nothing for scripts.
func progress(format string, args ...any) {
	if !jsonMode && isTerminal(os.Stderr) {
		fmt.Fprintln(os.Stderr, errStyle.dim(fmt.Sprintf(format, args...)))
	}
}

// style colours text when writing to a terminal. NO_COLOR turns colour off and
// CLICOLOR_FORCE or FORCE_COLOR turn it on, following no-color.org and the CLICOLOR convention.
type style struct{ on bool }

var (
	outStyle = newStyle(os.Stdout)
	errStyle = newStyle(os.Stderr)
)

func newStyle(f *os.File) style {
	switch {
	case os.Getenv("NO_COLOR") != "":
		return style{false}
	case forced(os.Getenv("CLICOLOR_FORCE")) || forced(os.Getenv("FORCE_COLOR")):
		return style{true}
	case os.Getenv("TERM") == "dumb":
		return style{false}
	}
	return style{isTerminal(f)}
}

func forced(v string) bool { return v != "" && v != "0" && v != "false" }

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func (s style) wrap(code, text string) string {
	if !s.on || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s style) bold(t string) string   { return s.wrap("1", t) }
func (s style) dim(t string) string    { return s.wrap("2", t) }
func (s style) red(t string) string    { return s.wrap("31", t) }
func (s style) green(t string) string  { return s.wrap("32", t) }
func (s style) yellow(t string) string { return s.wrap("33", t) }
func (s style) cyan(t string) string   { return s.wrap("36", t) }
func (s style) plain(t string) string  { return t }

// cell is one table cell: its text, the style to draw it in, and whether it aligns right.
type cell struct {
	text  string
	paint func(string) string
	right bool
}

func c(text string, paint func(string) string) cell { return cell{text: text, paint: paint} }

// table lines up cells by their visible width, which tabwriter can't do once text carries colour codes.
type table struct{ rows [][]cell }

func (t *table) add(cells ...cell) { t.rows = append(t.rows, cells) }

func (t *table) render(w io.Writer, indent string) {
	var widths []int
	for _, r := range t.rows {
		for i, cl := range r {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], utf8.RuneCountInString(cl.text))
		}
	}
	for _, r := range t.rows {
		var b strings.Builder
		b.WriteString(indent)
		// trailing empty cells would only add spaces
		last := len(r) - 1
		for last > 0 && r[last].text == "" {
			last--
		}
		for i := 0; i <= last; i++ {
			cl := r[i]
			pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cl.text))
			paint := cl.paint
			if paint == nil {
				paint = outStyle.plain
			}
			switch {
			case cl.right:
				b.WriteString(pad + paint(cl.text))
			case i == last:
				b.WriteString(paint(cl.text))
			default:
				b.WriteString(paint(cl.text) + pad)
			}
			if i < last {
				b.WriteString("  ")
			}
		}
		fmt.Fprintln(w, b.String())
	}
	t.rows = nil
}

// heading prints a section title with a short note on what the section means.
func heading(w io.Writer, title, note string) {
	fmt.Fprintf(w, "\n%s  %s\n", outStyle.bold(title), outStyle.dim(note))
}

// tildify shortens a path under the home folder to ~/...
func tildify(p string) string {
	if home != "" && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

// relTo shows a path relative to root when it's inside it, otherwise from the home folder.
func relTo(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return tildify(path)
}
