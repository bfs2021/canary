package main

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The owner-declared states file names conditions the owner already knows
// about: an app deliberately stopped, a bond-market holiday that leaves the
// usbond farm dark, a standing entitlement gap. Declared items are counted
// under `declared` and never raise attention. The monitor only reads this
// file; the owner writes it. A missing file means no declarations.

type statesFile struct {
	Version      int           `json:"version"`
	Declarations []declaration `json:"declarations"`
}

type declaration struct {
	// Match is compared, case-insensitively, with an item's keys (family,
	// kind, incident subject or farm name, each also prefixed with the log:
	// "app:log_coverage_stale") for equality, and with its redacted sample
	// as a substring.
	Match  string `json:"match"`
	Reason string `json:"reason"`
	// From and Until bound the declaration (inclusive dates, YYYY-MM-DD);
	// Dates lists single days instead, such as a market holiday. One of
	// Until or Dates is required: nothing is declared forever.
	From       string   `json:"from,omitempty"`
	Until      string   `json:"until,omitempty"`
	Dates      []string `json:"dates,omitempty"`
	DeclaredAt string   `json:"declared_at,omitempty"`
	DeclaredBy string   `json:"declared_by,omitempty"`
}

type declarationsReport struct {
	Path  string `json:"path"`
	State string `json:"state"` // missing | loaded | invalid
	// Active counts declarations valid for at least one day of this run;
	// Ignored counts entries lacking a match or a date bound.
	Loaded  int `json:"loaded,omitempty"`
	Ignored int `json:"ignored,omitempty"`
}

type declarations struct {
	report declarationsReport
	items  []declaration
}

func loadDeclarations(path string) declarations {
	d := declarations{report: declarationsReport{Path: path, State: "missing"}}
	if path == "" {
		d.report.State = "none"
		return d
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return d
	}
	var file statesFile
	if err != nil || json.Unmarshal(raw, &file) != nil || file.Version != 1 {
		d.report.State = "invalid"
		return d
	}
	d.report.State = "loaded"
	for _, decl := range file.Declarations {
		decl.Match = strings.ToLower(strings.TrimSpace(decl.Match))
		if decl.Match == "" || decl.Until == "" && len(decl.Dates) == 0 {
			d.report.Ignored++
			continue
		}
		d.items = append(d.items, decl)
	}
	d.report.Loaded = len(d.items)
	return d
}

// activeOn reports whether the declaration covers a calendar day.
func (decl declaration) activeOn(day string) bool {
	if len(decl.Dates) > 0 {
		return slices.Contains(decl.Dates, day)
	}
	if decl.From != "" && day < decl.From {
		return false
	}
	return day <= decl.Until
}

// matches compares a key-like match ("gateway", "app:log_coverage_stale",
// "usbond") with the item's keys only; a phrase (anything with a space or
// other punctuation) matches the item's redacted sample text. A one-word
// match never declares an unrelated family whose message merely mentions it.
func (decl declaration) matches(keys []string, text string) bool {
	if keyLike.MatchString(decl.Match) {
		for _, key := range keys {
			if strings.ToLower(key) == decl.Match {
				return true
			}
		}
		return false
	}
	return text != "" && strings.Contains(strings.ToLower(text), decl.Match)
}

var keyLike = regexp.MustCompile(`^[a-z0-9_.:-]+$`)

// find returns the declaration covering an item seen on any of days.
func (d declarations) find(keys []string, text string, days ...time.Time) (declaration, bool) {
	for _, decl := range d.items {
		if !decl.matches(keys, text) {
			continue
		}
		for _, day := range days {
			if !day.IsZero() && decl.activeOn(day.Format("2006-01-02")) {
				return decl, true
			}
		}
	}
	return declaration{}, false
}

// withLog adds the log-prefixed spelling of each key.
func withLog(log string, keys ...string) []string {
	out := slices.Clone(keys)
	for _, key := range keys {
		out = append(out, log+":"+key)
	}
	return out
}
