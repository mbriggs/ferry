package main

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// rewriteHome replaces a home-directory prefix in every JSON string literal of
// one JSON document. A string changes only when it equals oldHome or starts
// with oldHome+"/", so free text that mentions the path mid-string, like a
// shell command, is left alone. Everything outside the changed strings,
// including key order and number formatting, is kept byte for byte.
func rewriteHome(doc []byte, oldHome, newHome string) ([]byte, int, error) {
	if !json.Valid(doc) {
		return nil, 0, fmt.Errorf("not valid JSON")
	}
	var out bytes.Buffer
	n := 0
	for i := 0; i < len(doc); {
		if doc[i] != '"' {
			out.WriteByte(doc[i])
			i++
			continue
		}
		end := stringEnd(doc, i)
		lit := doc[i : end+1]
		i = end + 1
		var s string
		if err := json.Unmarshal(lit, &s); err != nil {
			return nil, 0, err
		}
		if s != oldHome && !hasPathPrefix(s, oldHome) {
			out.Write(lit)
			continue
		}
		enc, err := marshalString(newHome + s[len(oldHome):])
		if err != nil {
			return nil, 0, err
		}
		out.Write(enc)
		n++
	}
	return out.Bytes(), n, nil
}

// rewriteHomeLines applies rewriteHome to each non-empty line of a JSONL file.
func rewriteHomeLines(data []byte, oldHome, newHome string) ([]byte, int, error) {
	var out bytes.Buffer
	total := 0
	for lineNo, line := range bytes.Split(data, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		got, n, err := rewriteHome(line, oldHome, newHome)
		if err != nil {
			return nil, 0, fmt.Errorf("line %d: %w", lineNo+1, err)
		}
		out.Write(got)
		out.WriteByte('\n')
		total += n
	}
	return out.Bytes(), total, nil
}

func hasPathPrefix(s, prefix string) bool {
	return len(s) > len(prefix) && s[:len(prefix)] == prefix && s[len(prefix)] == '/'
}

// stringEnd returns the index of the closing quote of the string literal that
// opens at doc[start]. doc is known to be valid JSON.
func stringEnd(doc []byte, start int) int {
	for i := start + 1; i < len(doc); i++ {
		switch doc[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return len(doc) - 1
}

func marshalString(s string) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
