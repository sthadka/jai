package jql

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// tokenKind enumerates the lexical token categories produced by the lexer.
type tokenKind int

const (
	tEOF       tokenKind = iota
	tIdent               // bare word, GUID, cf[10234], quoted-or-not field name source
	tString              // quoted string literal
	tNumber              // numeric literal
	tRelDate             // relative-date literal, e.g. -90d
	tLParen              // (
	tRParen              // )
	tComma               // ,
	tEq                  // =
	tNeq                 // !=
	tGt                  // >
	tGte                 // >=
	tLt                  // <
	tLte                 // <=
	tContains            // ~
	tNContains           // !~

	// keyword tokens (matched case-insensitively)
	tAnd
	tOr
	tNot
	tIn
	tIs
	tEmpty
	tNull
	tOrder
	tBy
	tAsc
	tDesc
	tWas
	tChanged
	tDuring
	tBefore
	tAfter
)

// token is a single lexeme with its source byte offset. For tString the text is
// the already-unescaped content; for all others it is the verbatim source slice.
type token struct {
	kind tokenKind
	text string
	off  int
}

// keywords maps lowercased keyword spellings to their token kind. Any word not
// present here (and not numeric / relative-date shaped) lexes as tIdent, so
// field names, bare-word values, GUIDs and function names all fall through to
// tIdent.
var keywords = map[string]tokenKind{
	"and":     tAnd,
	"or":      tOr,
	"not":     tNot,
	"in":      tIn,
	"is":      tIs,
	"empty":   tEmpty,
	"null":    tNull,
	"order":   tOrder,
	"by":      tBy,
	"asc":     tAsc,
	"desc":    tDesc,
	"was":     tWas,
	"changed": tChanged,
	"during":  tDuring,
	"before":  tBefore,
	"after":   tAfter,
}

// lexError is returned for malformed input; it carries the offending offset.
type lexError struct {
	Offset int
	Msg    string
}

func (e *lexError) Error() string {
	return fmt.Sprintf("offset %d: %s", e.Offset, e.Msg)
}

// lexer turns a JQL source string into a flat token slice.
type lexer struct {
	src string
	pos int
}

// isWordChar reports whether r may appear inside a bare word. The set is wide on
// purpose so a GUID (`ec74d716-af36-…-1819`), a custom-field reference
// (`cf[10234]`), an Atlassian account id (`557058:abc-def`), a version
// (`1.0.3`) and a relative-date literal (`-90d`) all scan as a single word; the
// word is then classified by shape.
func isWordChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '_' || r == '.' || r == '-' || r == ':' || r == '[' || r == ']' || r == '+':
		return true
	default:
		return false
	}
}

// lex tokenizes the whole source, returning the token slice (always terminated
// by a tEOF token) or a *lexError.
func lex(src string) ([]token, error) {
	l := &lexer{src: src}
	var toks []token
	for {
		tok, err := l.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, tok)
		if tok.kind == tEOF {
			return toks, nil
		}
	}
}

// next scans and returns the next token.
func (l *lexer) next() (token, error) {
	l.skipSpace()
	if l.pos >= len(l.src) {
		return token{kind: tEOF, off: l.pos}, nil
	}
	start := l.pos
	c := l.src[l.pos]

	switch c {
	case '(':
		l.pos++
		return token{tLParen, "(", start}, nil
	case ')':
		l.pos++
		return token{tRParen, ")", start}, nil
	case ',':
		l.pos++
		return token{tComma, ",", start}, nil
	case '=':
		l.pos++
		return token{tEq, "=", start}, nil
	case '~':
		l.pos++
		return token{tContains, "~", start}, nil
	case '!':
		if l.peekAt(1) == '=' {
			l.pos += 2
			return token{tNeq, "!=", start}, nil
		}
		if l.peekAt(1) == '~' {
			l.pos += 2
			return token{tNContains, "!~", start}, nil
		}
		return token{}, &lexError{start, "unexpected '!' (did you mean '!=' or '!~'?)"}
	case '>':
		if l.peekAt(1) == '=' {
			l.pos += 2
			return token{tGte, ">=", start}, nil
		}
		l.pos++
		return token{tGt, ">", start}, nil
	case '<':
		if l.peekAt(1) == '=' {
			l.pos += 2
			return token{tLte, "<=", start}, nil
		}
		l.pos++
		return token{tLt, "<", start}, nil
	case '"', '\'':
		return l.lexString(c)
	}

	if isWordChar(rune(c)) {
		return l.lexWord(), nil
	}

	return token{}, &lexError{start, fmt.Sprintf("unexpected character %q", string(c))}
}

// skipSpace advances past ASCII whitespace.
func (l *lexer) skipSpace() {
	for l.pos < len(l.src) {
		switch l.src[l.pos] {
		case ' ', '\t', '\n', '\r', '\f', '\v':
			l.pos++
		default:
			return
		}
	}
}

// peekAt returns the byte at pos+n, or 0 past the end.
func (l *lexer) peekAt(n int) byte {
	if l.pos+n < len(l.src) {
		return l.src[l.pos+n]
	}
	return 0
}

// lexString scans a single- or double-quoted string. A backslash escapes the
// next character (so `\"` and `\\` are literal). An unterminated string is an
// error.
func (l *lexer) lexString(quote byte) (token, error) {
	start := l.pos
	l.pos++ // opening quote
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		if c == '\\' && l.pos+1 < len(l.src) {
			b.WriteByte(l.src[l.pos+1])
			l.pos += 2
			continue
		}
		if c == quote {
			l.pos++ // closing quote
			return token{tString, b.String(), start}, nil
		}
		b.WriteByte(c)
		l.pos++
	}
	return token{}, &lexError{start, "unterminated string literal"}
}

// lexWord scans a bare word and classifies it as a relative date, number,
// keyword or identifier.
func (l *lexer) lexWord() token {
	start := l.pos
	for l.pos < len(l.src) {
		r, size := utf8.DecodeRuneInString(l.src[l.pos:])
		if !isWordChar(r) {
			break
		}
		l.pos += size
	}
	text := l.src[start:l.pos]

	if isRelativeDate(text) {
		return token{tRelDate, text, start}
	}
	if isNumber(text) {
		return token{tNumber, text, start}
	}
	if kw, ok := keywords[strings.ToLower(text)]; ok {
		return token{kw, text, start}
	}
	return token{tIdent, text, start}
}

// isNumber reports whether text is an integer or decimal literal with an
// optional leading sign.
func isNumber(text string) bool {
	if text == "" {
		return false
	}
	i := 0
	if text[0] == '+' || text[0] == '-' {
		i++
	}
	if i >= len(text) {
		return false
	}
	seenDot := false
	seenDigit := false
	for ; i < len(text); i++ {
		c := text[i]
		switch {
		case c >= '0' && c <= '9':
			seenDigit = true
		case c == '.' && !seenDot:
			seenDot = true
		default:
			return false
		}
	}
	return seenDigit
}

// relativeUnits are the valid trailing units for a relative-date literal. Note
// the case distinction between minutes ("m") and months ("M"), matching Jira.
var relativeUnits = map[string]bool{
	"s": true, "m": true, "h": true, "d": true, "w": true, "M": true, "y": true,
}

// isRelativeDate reports whether text matches `[+-]?<digits><unit>` where unit
// is one of the Jira relative-date units.
func isRelativeDate(text string) bool {
	if text == "" {
		return false
	}
	i := 0
	if text[0] == '+' || text[0] == '-' {
		i++
	}
	digitStart := i
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	if i == digitStart { // no digits
		return false
	}
	unit := text[i:]
	return relativeUnits[unit]
}
