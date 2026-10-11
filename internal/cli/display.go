package cli

import (
	"fmt"
	"io"
	"strings"
)

// displayLine keeps untrusted text out of terminal control sequences. Styling
// is applied only after wrapping, so a narrow terminal cannot split an escape.
func displayLine(env *Env, text string, style func(string) string) {
	indent := text[:len(text)-len(strings.TrimLeft(text, " "))]
	riskDisplayLine(env, indent, strings.TrimLeft(text, " "), style)
}

func displayRow(env *Env, out io.Writer, label, value string) {
	labeledRow(env, out, "", 15, label, sanitizeRunText(value))
}

// labeledRow keeps every value, wraps beneath its column, and gives long
// labels their own line. Values may already carry the renderer's SGR styles.
func labeledRow(env *Env, out io.Writer, indent string, column int, label, value string) {
	width := terminalTextWidth(out)
	if visibleLen(label) >= column {
		for _, line := range wrapVisibleText(label, max(1, width-len(indent))) {
			fmt.Fprintln(out, indent+env.dim(line))
		}
		label = ""
	}
	prefix := indent + env.dim(padRightVisible(label, column-1)) + " "
	continuation := strings.Repeat(" ", len(indent)+column)
	for i, line := range wrapVisibleText(value, max(1, width-len(indent)-column)) {
		if i > 0 {
			prefix = continuation
		}
		fmt.Fprintln(out, prefix+line)
	}
}

func displayNumber(value *float64, scale float64, format string) string {
	if value == nil {
		return "—"
	}
	return fmt.Sprintf(format, *value*scale)
}

// displayWrapped preserves diagnostic lines and their indentation, while
// bounding the older full-detail renderers to the current terminal width.
func displayWrapped(env *Env, text string) {
	for line := range strings.SplitSeq(strings.TrimSuffix(text, "\n"), "\n") {
		indent := strings.Repeat(" ", min(6, len(line)-len(strings.TrimLeft(line, " "))))
		for _, part := range wrapVisibleText(sanitizeRunText(strings.TrimSpace(line)), max(1, briefProseWidth(env.Stdout)-len(indent))) {
			fmt.Fprintln(env.Stdout, indent+part)
		}
	}
}
