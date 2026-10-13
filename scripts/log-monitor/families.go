package main

import (
	"regexp"
	"strings"
)

// A family is the unit the report counts, carries across runs, declares and
// judges new: a short lowercase key such as "gamma", "market_history" or
// "broker_code_200_no_definition". Message shapes vary per attempt (a count,
// a contract, an elapsed time); a family does not, so one failing subsystem is
// one row however many shapes its lines take.

var (
	// Connector prefixes name the client, not the subsystem.
	connectorPrefix = regexp.MustCompile(`^(?:\s*(?:\[IBKR cid=\d+\]|IBKRConnector:|\[cid=\d+\]))+\s*`)
	noticeCode      = regexp.MustCompile(`\bcode=(\d+)`)
	familyWord      = regexp.MustCompile(`^[a-z][a-z_-]*$`)
	digitRun        = regexp.MustCompile(`\d+`)
	clientNumber    = regexp.MustCompile(`\bClient(?: ID:)? \d+\b`)
)

// genericFamily derives a family from a log message: the leading lowercase
// words (at most two, stopping at a colon, a key=value field, a name or a
// number). A dotted first token keeps its first segment, so gamma.prewarm,
// gamma.abort and gamma.combine are one "gamma" family. Only lowercase words
// survive, so a family key never names a contract or an account.
func genericFamily(message string) string {
	message = connectorPrefix.ReplaceAllString(message, "")
	if strings.Contains(message, "System notice") {
		if m := noticeCode.FindStringSubmatch(message); m != nil {
			return "broker_code_" + m[1]
		}
	}
	var words []string
	for i, tok := range strings.Fields(message) {
		if i == 0 && tok != strings.ToUpper(tok) {
			// Sentence case ("Historical data") and camelCase ("WhatIf")
			// fold; an all-capitals name ("SYNTHQ") never becomes a key.
			tok = strings.ToLower(tok)
			if head, _, dotted := strings.Cut(tok, "."); dotted && familyWord.MatchString(head) {
				words = append(words, head)
				break
			}
		}
		stop := strings.HasSuffix(tok, ":")
		tok = strings.TrimRight(tok, ":;,")
		if !familyWord.MatchString(tok) {
			break
		}
		words = append(words, tok)
		if stop || len(words) == 2 {
			break
		}
	}
	if len(words) == 0 {
		return "other"
	}
	return strings.Join(words, "_")
}

// subsystemOf is the first word of a generic family; a failure run clusters a
// subsystem's families (gamma, gamma_compute) into one incident. Closed broker
// notice families are their own subsystem.
func subsystemOf(family string) string {
	if strings.HasPrefix(family, "broker_code_") {
		return family
	}
	head, _, _ := strings.Cut(family, "_")
	return head
}

// normalizeCounts replaces the counts and ordinals that vary per occurrence
// ("loss 30 this session", "7 cancel echoes", "29 losses over …", "Client
// 15:") with N, so repeats of one message collapse into one sample. Numbers
// that tell broker conditions apart stay: a code ("code 2103", "code=200",
// "IBKR 200", "1101/1102"), a status, and any number glued to letters ("1D",
// "365d", "v3").
func normalizeCounts(message string) string {
	message = clientNumber.ReplaceAllString(message, "Client N")
	var out strings.Builder
	last := 0
	for _, m := range digitRun.FindAllStringIndex(message, -1) {
		a, b := m[0], m[1]
		if keepNumber(message, a, b) {
			continue
		}
		out.WriteString(message[last:a])
		out.WriteString("N")
		last = b
	}
	if last == 0 {
		return message
	}
	out.WriteString(message[last:])
	return out.String()
}

func keepNumber(s string, a, b int) bool {
	if a > 0 && isWordByte(s[a-1]) || b < len(s) && isWordByte(s[b]) {
		return true
	}
	// Code pairs such as "1101/1102", and parts of dotted or clock values
	// the earlier normalizers left alone.
	if a > 0 && (s[a-1] == '/' || s[a-1] == '.') || b < len(s) && (s[b] == '/' || s[b] == '.' && b+1 < len(s) && s[b+1] >= '0' && s[b+1] <= '9') {
		return true
	}
	before := strings.ToLower(strings.TrimRight(s[:a], " =:#("))
	for _, word := range []string{"code", "codes", "ibkr", "status", "http", "error", "errorcode"} {
		if strings.HasSuffix(before, word) {
			return true
		}
	}
	return false
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
