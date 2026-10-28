// jslex_test.go
// Minimal JavaScript lexer for the i18n guard tests: string and template literals with context
// Version: 2026.10.28

package web

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// jsString is a string or template literal found in a JavaScript source.
type jsString struct {
	Line  int
	Quote byte   // '\'', '"' or '`'
	Text  string // decoded text; in template literals every ${…} is replaced by "\x00"
	// Before holds the code right before the literal (other literals as "",
	// whitespace collapsed), so callers can tell t('key') from el.title = '…'.
	Before string
}

type jsLexer struct {
	src  string
	pos  int
	line int
	out  []jsString
	code []byte
}

// lexJS returns every string and template literal of src, including those nested in ${…}.
func lexJS(src string) ([]jsString, error) {
	l := &jsLexer{src: src, line: 1}
	if err := l.run(false); err != nil {
		return nil, err
	}
	return l.out, nil
}

func (l *jsLexer) emit(c byte) {
	if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
		if n := len(l.code); n > 0 && l.code[n-1] == ' ' {
			return
		}
		c = ' '
	}
	l.code = append(l.code, c)
}

func (l *jsLexer) before() string {
	s := strings.TrimSpace(string(l.code))
	if len(s) > 80 {
		s = s[len(s)-80:]
	}
	return s
}

// regexAllowed reports whether a '/' at the current position starts a regular expression.
func (l *jsLexer) regexAllowed() bool {
	s := strings.TrimRight(string(l.code), " ")
	if s == "" {
		return true
	}
	last := s[len(s)-1]
	// Postfix ++ / -- (after an identifier, ')' or ']') ends an operand: a
	// following '/' divides.
	if strings.HasSuffix(s, "++") || strings.HasSuffix(s, "--") {
		if op := strings.TrimRight(s[:len(s)-2], " "); op != "" {
			if c := op[len(op)-1]; isIdentByte(c) || c == ')' || c == ']' {
				return false
			}
		}
	}
	if strings.IndexByte("(,=:[!&|?{};+-*%<>~^", last) >= 0 {
		return true
	}
	i := len(s)
	for i > 0 && (isIdentByte(s[i-1])) {
		i--
	}
	switch s[i:] {
	case "return", "typeof", "case", "in", "of", "delete", "void", "throw", "new", "else", "do":
		return true
	}
	return false
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func (l *jsLexer) errorf(format string, a ...any) error {
	return fmt.Errorf("line %d: %s", l.line, fmt.Sprintf(format, a...))
}

func (l *jsLexer) run(untilBrace bool) error {
	depth := 0
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		var next byte
		if l.pos+1 < len(l.src) {
			next = l.src[l.pos+1]
		}
		switch {
		case c == '\n':
			l.line++
			l.emit(' ')
			l.pos++
		case c == '/' && next == '/':
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
		case c == '/' && next == '*':
			end := strings.Index(l.src[l.pos+2:], "*/")
			if end < 0 {
				return l.errorf("unterminated comment")
			}
			l.line += strings.Count(l.src[l.pos:l.pos+2+end], "\n")
			l.pos += end + 4
			l.emit(' ')
		case c == '/' && l.regexAllowed():
			if err := l.skipRegex(); err != nil {
				return err
			}
		case c == '\'' || c == '"':
			if err := l.readString(c); err != nil {
				return err
			}
		case c == '`':
			if err := l.readTemplate(); err != nil {
				return err
			}
		case c == '{':
			depth++
			l.emit(c)
			l.pos++
		case c == '}':
			if untilBrace && depth == 0 {
				l.pos++
				return nil
			}
			depth--
			l.emit(c)
			l.pos++
		default:
			l.emit(c)
			l.pos++
		}
	}
	if untilBrace {
		return l.errorf("unterminated ${")
	}
	return nil
}

func (l *jsLexer) skipRegex() error {
	l.pos++
	inClass := false
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\n':
			return l.errorf("unterminated regular expression")
		case c == '\\':
			l.pos += 2
			continue
		case c == '[':
			inClass = true
		case c == ']':
			inClass = false
		case c == '/' && !inClass:
			l.pos++
			for l.pos < len(l.src) && isIdentByte(l.src[l.pos]) {
				l.pos++
			}
			l.code = append(l.code, '/', 'r', '/')
			return nil
		}
		l.pos++
	}
	return l.errorf("unterminated regular expression")
}

// readEscape decodes the escape sequence at l.pos (the backslash) and appends it to b.
func (l *jsLexer) readEscape(b *strings.Builder) {
	l.pos++
	if l.pos >= len(l.src) {
		return
	}
	c := l.src[l.pos]
	l.pos++
	switch c {
	case 'n':
		b.WriteByte('\n')
	case 't':
		b.WriteByte('\t')
	case 'r':
		b.WriteByte('\r')
	case '0':
		b.WriteByte(0)
	case '\r':
		if l.pos < len(l.src) && l.src[l.pos] == '\n' {
			l.pos++
		}
		l.line++
	case '\n':
		l.line++
	case 'x':
		if l.pos+2 <= len(l.src) {
			if v, err := strconv.ParseUint(l.src[l.pos:l.pos+2], 16, 32); err == nil {
				b.WriteRune(rune(v))
				l.pos += 2
			}
		}
	case 'u':
		if l.pos < len(l.src) && l.src[l.pos] == '{' {
			end := strings.IndexByte(l.src[l.pos:], '}')
			if end > 0 {
				if v, err := strconv.ParseUint(l.src[l.pos+1:l.pos+end], 16, 32); err == nil {
					b.WriteRune(rune(v))
				}
				l.pos += end + 1
			}
		} else if l.pos+4 <= len(l.src) {
			if v, err := strconv.ParseUint(l.src[l.pos:l.pos+4], 16, 32); err == nil {
				b.WriteRune(rune(v))
				l.pos += 4
			}
		}
	default:
		b.WriteByte(c)
	}
}

func (l *jsLexer) readString(q byte) error {
	start := l.line
	before := l.before()
	l.pos++
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch c {
		case '\\':
			l.readEscape(&b)
			continue
		case '\n':
			return l.errorf("unterminated string starting on line %d", start)
		case q:
			l.pos++
			l.out = append(l.out, jsString{Line: start, Quote: q, Text: b.String(), Before: before})
			l.code = append(l.code, '"', '"')
			return nil
		}
		b.WriteByte(c)
		l.pos++
	}
	return l.errorf("unterminated string starting on line %d", start)
}

func (l *jsLexer) readTemplate() error {
	start := l.line
	before := l.before()
	l.pos++
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\\':
			l.readEscape(&b)
			continue
		case c == '`':
			l.pos++
			l.out = append(l.out, jsString{Line: start, Quote: '`', Text: b.String(), Before: before})
			l.code = append(l.code, '"', '"')
			return nil
		case c == '$' && l.pos+1 < len(l.src) && l.src[l.pos+1] == '{':
			b.WriteByte(0)
			l.pos += 2
			saved := l.code
			l.code = append([]byte(nil), l.code...)
			l.code = append(l.code, '$', '{')
			if err := l.run(true); err != nil {
				return err
			}
			l.code = saved
			continue
		case c == '\n':
			l.line++
		}
		b.WriteByte(c)
		l.pos++
	}
	return l.errorf("unterminated template literal starting on line %d", start)
}

// TestLexJSDivision: a '/' after an operand divides; only after an operator
// or keyword does it start a regular expression.
func TestLexJSDivision(t *testing.T) {
	for src, want := range map[string]string{
		"a++ / 2; 'x'; b / 3":            "x",
		"a[i]-- / 2; 'x'; b / 3":         "x",
		"(a.b)++ / 2; 'x'; b / 3":        "x",
		"n = c / 2; 'x'; d / 4":          "x",
		"if (/^'q'$/.test(s)) t('x');":   "x",
		"return /'q'/.test(s) ? 'x' : 0": "x",
		"x = ++/'q'/.lastIndex; 'x'":     "x",
	} {
		lits, err := lexJS(src)
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		var got []string
		for _, l := range lits {
			got = append(got, l.Text)
		}
		if strings.Join(got, "|") != want {
			t.Errorf("lexJS(%s) = %q, want %q", src, got, want)
		}
	}
}
