package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/osauer/canary/v2/internal/productidentity"
)

// Help is presentation metadata only. Handlers still own flags, parsing and gates.
type actionHelp struct{ name, summary, usage string }

func commandActions(name string) []actionHelp {
	switch name {
	case "order":
		return []actionHelp{
			{"preview", "Check an order without submitting it", "canary order preview buy|sell SYMBOL QTY [flags]\ncanary order preview buy|sell SYMBOL YYYYMMDD C|P STRIKE QTY [flags]\ncanary order preview buy|sell ISIN|CUSIP FACE --type BOND|BILL --currency CCY [flags]"},
			{"status", "Read one order from the local journal", "canary order status ID [--json]"},
			{"place", "Submit an eligible preview through the trading gates", "canary order place --preview-token TOKEN [--json]"},
			{"modify", "Replace an order using an eligible preview", "canary order modify ID --preview-token TOKEN [--json]"},
			{"cancel", "Request an order cancellation through the trading gates", "canary order cancel ID [--json]"},
		}
	case "orders":
		return []actionHelp{
			{"open", "Read tracked orders and the broker open-order inventory", "canary orders open [--json]"},
			{"history", "Read order history from the local journal", "canary orders history [--since DATE|TIME] [--until DATE|TIME] [--limit N] [--event-limit N] [--json]"},
		}
	case "proposals":
		return []actionHelp{
			{"status", "Read proposal monitoring and trading status", "canary proposals status [--json]"},
			{"refresh", "Refresh protection proposals without submitting orders", "canary proposals refresh [--details] [--json]"},
			{"list", "Read current protection proposals", "canary proposals list [--details] [--json]"},
			{"preview", "Check a proposal without submitting it", "canary proposals preview KEY REVISION [flags]"},
			{"prepare", "Prepare a private proposal handoff; no submission", "canary proposals prepare KEY REVISION --json [flags]"},
			{"prepared-status", "Read a preparation receipt using its private reference", "canary proposals prepared-status --prepared-ref-stdin [--json]"},
			{"prepare-bundle", "Prepare a private bundle handoff; no submission", "canary proposals prepare-bundle BUNDLE_ID REVISION [--json]"},
			{"submit-bundle", "Submit an approved bundle through the trading gates", "canary proposals submit-bundle --stdin [--json]"},
			{"bundle-status", "Read a bundle receipt using its private reference", "canary proposals bundle-status --bundle-ref-stdin [--json]"},
			{"submit", "Submit an approved proposal through the trading gates", "canary proposals submit KEY REVISION [flags]"},
			{"reduce", "Preview a reduction; --submit requests a gated order", "canary proposals reduce [SYMBOL] --percent N [--con-id ID] [flags]\ncanary proposals reduce --portfolio --percent N [flags]"},
			{"request-stop", "Draft a protective stop without placing it", "canary proposals request-stop [SYMBOL] [--con-id ID] [--json]"},
			{"ignore", "Record that a proposal should be ignored", "canary proposals ignore KEY [REVISION] [--reason TEXT] [--json]"},
			{"veto", "Stop a pending pre-authorised submission; human only", "canary proposals veto KEY [REVISION] [--reason TEXT] [--json]"},
			{"queue", "Prepare, arm, cancel or read an owner-approved order queue", "canary proposals queue <action> [flags]"},
		}
	case "proposals queue":
		return []actionHelp{
			{"prepare", "Prepare a private queued-order handoff; no submission", "canary proposals queue prepare KEY REVISION --json [--quantity N]\ncanary proposals queue prepare --stdin --json"},
			{"arm", "Arm an approved queued order from standard input", "canary proposals queue arm --stdin [--json]"},
			{"cancel", "Cancel a queued authorisation", "canary proposals queue cancel QUEUE_ID [--json]\ncanary proposals queue cancel --all [--json]"},
			{"list", "Read queued authorisations", "canary proposals queue list [--live] [--json]"},
			{"status", "Read one queued authorisation", "canary proposals queue status QUEUE_ID [--json]"},
		}
	}
	return nil
}

func helpDescription(name string) (summary, usage string) {
	if cmd, ok := lookupCommand(name); ok {
		return cmd.Summary, cmd.Usage
	}
	if parent, action, ok := strings.Cut(name, " "); ok {
		// Match the longest named parent, including proposals queue.
		if actionParent, actionName, found := strings.CutLast(name, " "); found {
			for _, entry := range commandActions(actionParent) {
				if entry.name == actionName {
					return entry.summary, entry.usage
				}
			}
		}
		if cmd, ok := lookupCommand(parent); ok {
			var lines []string
			for line := range strings.SplitSeq(cmd.Usage, " | ") {
				if strings.HasPrefix(line, productidentity.Executable+" "+name+" ") || line == productidentity.Executable+" "+name {
					lines = append(lines, line)
				}
			}
			if len(lines) > 0 {
				return "", strings.Join(lines, "\n")
			}
			return "", productidentity.Executable + " " + parent + " " + action + " [flags]"
		}
	}
	return "", ""
}

func renderUsage(env *Env) {
	w := env.Stdout
	helpLine(env, "", productidentity.ProductName+" — portfolio, risk and order tools for Interactive Brokers", env.bold)
	helpLine(env, "", "Runs locally. Sending or changing orders requires approval and safety checks.", nil)
	fmt.Fprintln(w)
	helpLine(env, "Usage: ", productidentity.Executable+" <subcommand> [flags] [args]", env.cyan)
	helpLine(env, "Start: ", productidentity.Executable+" status — check the broker connection", nil)
	specs := Catalog()
	for _, group := range HelpGroups() {
		fmt.Fprintln(w)
		helpLine(env, "", group.Title, env.bold)
		helpLine(env, "", group.Tagline, env.dim)
		fmt.Fprintln(w)
		for _, spec := range specs {
			if spec.Group == group.Group {
				helpRow(env, spec.Name, spec.listing(), 18)
			}
		}
	}
	fmt.Fprintln(w)
	helpLine(env, "", "Run canary <command> --help for usage and available actions.", nil)
	helpLine(env, "", "Run canary <command> <action> --help for that action's flags.", nil)
	helpLine(env, "", "Add --json to data commands for output that scripts can read.", nil)
	fmt.Fprintln(w)
	helpLine(env, "", "Colour: NO_COLOR=1 disables it; CANARY_COLOR=always|never overrides.", env.dim)
}

func helpLine(env *Env, prefix, text string, style func(string) string) {
	width := terminalTextWidth(env.Stdout)
	for i, line := range wrapVisibleText(text, max(1, width-visibleLen(prefix))) {
		if style != nil {
			line = style(line)
		}
		if i > 0 {
			prefix = strings.Repeat(" ", visibleLen(prefix))
		}
		fmt.Fprintln(env.Stdout, prefix+line)
	}
}

func helpRow(env *Env, label, value string, column int) {
	prefix := "  " + env.cyan(padRightVisible(label, column-2))
	if visibleLen(label) > column-3 {
		helpLine(env, "  ", label, env.cyan)
		prefix = strings.Repeat(" ", column)
	}
	helpLine(env, prefix, value, nil)
}

func renderCommandHelp(env *Env, fs *flag.FlagSet, name, summary, usage string) {
	w := env.Stdout
	title := productidentity.Executable + " " + name
	if summary != "" {
		title += " — " + summary
	}
	helpLine(env, "", title, env.bold)
	fmt.Fprintln(w)
	actions := commandActions(name)
	if len(actions) > 0 {
		helpLine(env, "Usage: ", productidentity.Executable+" "+name+" <action> [flags]", env.cyan)
		fmt.Fprintln(w, "\n"+env.bold("Actions"))
		for _, a := range actions {
			helpRow(env, a.name, a.summary, 20)
		}
		fmt.Fprintln(w)
		helpLine(env, "", "Run "+productidentity.Executable+" "+name+" <action> --help for syntax and flags.", nil)
	} else if usage != "" {
		// Only separators between complete commands split usage; buy|sell and
		// other argument alternatives remain on the same syntax line.
		usage = strings.ReplaceAll(usage, " | canary ", "\ncanary ")
		for i, line := range strings.Split(usage, "\n") {
			prefix := "       "
			if i == 0 {
				prefix = "Usage: "
			}
			helpLine(env, prefix, line, env.cyan)
		}
	}
	var flags []*flag.Flag
	fs.VisitAll(func(f *flag.Flag) { flags = append(flags, f) })
	if len(flags) > 0 {
		fmt.Fprintln(w, "\n"+env.bold("Flags"))
		for _, f := range flags {
			value, description := flag.UnquoteUsage(f)
			label := "--" + f.Name
			if value != "" {
				label += " " + strings.ToUpper(value)
			}
			if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" && f.DefValue != "0s" && !strings.Contains(description, "default") {
				description += " (default: " + f.DefValue + ")"
			}
			helpRow(env, label, description, 28)
		}
	}
	fmt.Fprintln(w)
}

func terminalTextWidth(w io.Writer) int {
	if width := outputColumns(w); width > 0 {
		return width
	}
	return 80
}
