package uploads

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"

	"github.com/go-enry/go-enry/v2"
)

const maxClassifyBytes = 512 * 1024

var (
	reGo        = regexp.MustCompile(`(?m)^\s*package\s+[a-zA-Z_][a-zA-Z0-9_]*\s*$`)
	reRust      = regexp.MustCompile(`(?m)(^\s*(pub\s+)?fn\s+\w+|^\s*use\s+std::)`)
	reJava      = regexp.MustCompile(`(?m)(^\s*public\s+class\s+|^\s*import\s+java\.)`)
	rePy        = regexp.MustCompile(`(?m)(^\s*from\s+\w+\s+import\s+|^\s*import\s+\w+|^\s*def\s+\w+\s*\(|^\s*class\s+\w+\s*[:(\n])`)
	rePHP       = regexp.MustCompile(`(?s)<\?php|\bnamespace\s+\w+;`)
	reJSModule  = regexp.MustCompile(`(?m)^\s*(import|export)\s+.+from\s+['"]`)
	reTS        = regexp.MustCompile(`(?m)(^\s*(interface|type)\s+\w+|:\s*(string|number|boolean|void)\s*[;=\)]|as\s+const\s*;)`)
	reHTML      = regexp.MustCompile(`(?is)<!DOCTYPE\s+html|<\s*html[\s/>]`)
	reXML       = regexp.MustCompile(`(?s)^\s*<\?xml\s+version`)
	reSQL       = regexp.MustCompile(`(?is)\bSELECT\s+[\w*]+\s+FROM\s+\w+|\bINSERT\s+INTO\s+\w+|\bUPDATE\s+\w+\s+SET\b`)
	reCSS       = regexp.MustCompile(`(?m)(^|\n)\s*[.#][a-zA-Z_-][a-zA-Z0-9_-]*\s*\{`)
	reMarkdown  = regexp.MustCompile(`(?m)^#{1,6}\s+\S`)
	reShell     = regexp.MustCompile(`(?m)^\s*#!/usr/bin/env\s+\w+sh|^\s*#!/bin/(ba)?sh\b`)
	reCMake     = regexp.MustCompile(`(?m)^\s*cmake_minimum_required\s*\(`)
	reMakefile  = regexp.MustCompile(`(?m)^\s*\.PHONY:|^\s*[\w%-]+:\s*(\n|\s|#)`)
)

// ClassifyPasteText returns a short display label: a language name (as in GitHub Linguist) or "plain text".
func ClassifyPasteText(content []byte) string {
	if len(content) == 0 {
		return "plain text"
	}
	if enry.IsBinary(content) {
		return "plain text"
	}
	body := content
	if len(body) > maxClassifyBytes {
		body = body[:maxClassifyBytes]
	}
	if l := strings.TrimSpace(enry.GetLanguage("", body)); l != "" && !isProseLike(l) {
		return l
	}
	s := string(body)
	if hasManyWords(s) {
		return "plain text"
	}
	if reHTML.MatchString(s) {
		return "HTML"
	}
	if reXML.MatchString(s) {
		return "XML"
	}
	if rePHP.MatchString(s) {
		return "PHP"
	}
	if reCMake.MatchString(s) {
		return "CMake"
	}
	if reMakefile.MatchString(s) {
		return "Makefile"
	}
	if reShell.MatchString(s) {
		return "Shell"
	}
	if bytes.Contains(body, []byte("#include")) {
		if bytes.Contains(body, []byte("namespace ")) || bytes.Contains(body, []byte("template")) ||
			bytes.Contains(body, []byte("std::")) || (bytes.Contains(body, []byte("class ")) && bytes.Contains(body, []byte("public:"))) {
			return "C++"
		}
		return "C"
	}
	if reRust.MatchString(s) {
		return "Rust"
	}
	if reGo.MatchString(s) && (bytes.Contains(body, []byte("func ")) || bytes.Contains(body, []byte("import ")) || bytes.Contains(body, []byte("import("))) {
		return "Go"
	}
	if reJava.MatchString(s) {
		return "Java"
	}
	if rePy.MatchString(s) {
		return "Python"
	}
	if reTS.MatchString(s) || (reJSModule.MatchString(s) && bytes.Contains(body, []byte(": "))) {
		return "TypeScript"
	}
	if reJSModule.MatchString(s) || regexp.MustCompile(`(?m)(^\s*function\s+\w+\s*\(|^\s*const\s+\w+\s*=\s*\(|^\s*let\s+\w+\s*=)`).MatchString(s) {
		return "JavaScript"
	}
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid(trimmed) {
		return "JSON"
	}
	if looksLikeYAML(s) {
		return "YAML"
	}
	if reSQL.MatchString(s) {
		return "SQL"
	}
	if reCSS.MatchString(s) {
		return "CSS"
	}
	if reMarkdown.MatchString(s) {
		return "Markdown"
	}
	if looksLikeRuby(s) {
		return "Ruby"
	}
	if regexp.MustCompile(`(?m)^\s*local\s+function\s+|\bfunction\s+[a-zA-Z_]\w*\s*\(`).MatchString(s) {
		return "Lua"
	}
	return "plain text"
}

func isProseLike(name string) bool {
	switch name {
	case "Text", "Markdown", "AsciiDoc", "TeX", "Rich Text Format":
		return true
	default:
		return false
	}
}

func looksLikeYAML(s string) bool {
	lines := strings.Split(s, "\n")
	if len(lines) == 0 {
		return false
	}
	first := strings.TrimSpace(lines[0])
	if first == "---" || first == "..." {
		return true
	}
	// key: value (no tabs in first 20 lines, common in YAML configs)
	n := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n++
		if n > 24 {
			break
		}
		if yamlKeyLine.MatchString(line) {
			return true
		}
	}
	return false
}

var yamlKeyLine = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*:\s`)

func looksLikeRuby(s string) bool {
	if !regexp.MustCompile(`(?m)(^\s*(class|module|def)\s+)`).MatchString(s) {
		return false
	}
	return strings.Contains(s, "end\n") || strings.HasSuffix(strings.TrimSpace(s), "end")
}

func hasManyWords(s string) bool {
	fields := strings.Fields(s)
	if len(fields) < 12 {
		return false
	}
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters < 40 {
		return false
	}
	codeish := 0
	for _, r := range s {
		switch r {
		case '{', '}', ';', '(', ')', '=', '<', '>', '[', ']', '`':
			codeish++
		}
	}
	return codeish < 3
}
