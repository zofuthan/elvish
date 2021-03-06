//go:generate stringer -type ItemType

// Derived from stdlib package text/template/parse.

// Copyright 2011 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package parse

import (
	"fmt"
	"unicode/utf8"
)

// Item represents a token or text string returned from the scanner.
type Item struct {
	Typ ItemType // The type of this Item.
	Pos Pos      // The starting position, in bytes, of this Item in the input string.
	Val string   // The value of this Item.
	End ItemEnd  // How an Item ends.
}

func (i Item) String() string {
	switch i.Typ {
	case ItemError:
		return "error: " + i.Val
	case ItemEOF:
		return "eof"
	default:
		return fmt.Sprintf("%q", i.Val)
	}
}

// GoString returns the Go representation of an Item.
func (i Item) GoString() string {
	return fmt.Sprintf("parse.Item{%s, %d, %q, %d}", i.Typ, i.Pos, i.Val, i.End)
}

// ItemType identifies the type of lex items.
type ItemType int

// ItemType constants.
const (
	ItemError ItemType = iota // error occurred; value is text of error

	ItemEOF               // end of file, always the last Item yielded
	ItemEndOfLine         // a single EOL
	ItemSpace             // run of spaces separating arguments
	ItemBare              // a bare string literal
	ItemSingleQuoted      // a single-quoted string literal
	ItemDoubleQuoted      // a double-quoted string literal
	ItemRedirLeader       // IO redirection leader
	ItemStatusRedirLeader // status redirection leader, "?>"
	ItemPipe              // pipeline connector, '|'
	ItemQuestionLParen    // question + left paren "?("
	ItemLParen            // left paren '('
	ItemRParen            // right paren ')'
	ItemLBracket          // left bracket '['
	ItemRBracket          // right bracket ']'
	ItemLBrace            // left brace '{'
	ItemRBrace            // right brace '}'
	ItemDollar            // dollar sign '$'
	ItemSemicolon         // semicolon ';'
	ItemAmpersand         // ampersand '&'
	ItemSigil             // one of predefined sigils
)

// ItemEnd describes the ending of lex items.
type ItemEnd int

// ItemEnd constants.
const (
	MayTerminate ItemEnd = 1 << iota
	MayContinue
	ItemTerminated   ItemEnd = MayTerminate
	ItemUnterminated ItemEnd = MayContinue
	ItemAmbiguous    ItemEnd = MayTerminate | MayContinue
)

const eof = -1

// stateFn represents the state of the scanner as a function that returns the next state.
type stateFn func(*Lexer) stateFn

// Lexer holds the state of the scanner.
type Lexer struct {
	name    string    // the name of the input; used only for error reports
	input   string    // the string being scanned
	state   stateFn   // the next lexing function to enter
	pos     Pos       // current position in the input
	start   Pos       // start position of this Item
	width   Pos       // width of last rune read from input
	lastPos Pos       // position of most recent Item returned by NextItem
	items   chan Item // channel of scanned items
}

// next returns the next rune in the input.
func (l *Lexer) next() rune {
	if int(l.pos) >= len(l.input) {
		l.width = 0
		return eof
	}
	r, w := utf8.DecodeRuneInString(l.input[l.pos:])
	l.width = Pos(w)
	l.pos += l.width
	return r
}

// peek returns but does not consume the next rune in the input.
func (l *Lexer) peek() rune {
	r := l.next()
	l.backup()
	return r
}

// backup steps back one rune. Can only be called once per call of next.
func (l *Lexer) backup() {
	l.pos -= l.width
}

// emit passes an Item back to the client.
func (l *Lexer) emit(t ItemType, e ItemEnd) {
	l.items <- Item{t, l.start, l.input[l.start:l.pos], e}
	l.start = l.pos
}

// NextItem returns the next Item from the input.
func (l *Lexer) NextItem() Item {
	item := <-l.items
	l.lastPos = item.Pos
	return item
}

// Chan returns a channel of Item's.
func (l *Lexer) Chan() chan Item {
	return l.items
}

// Lex creates a new scanner for the input string.
func Lex(name, input string) *Lexer {
	l := &Lexer{
		name:  name,
		input: input,
		items: make(chan Item),
	}
	go l.run()
	return l
}

// run runs the state machine for the Lexer.
func (l *Lexer) run() {
	for l.state = lexAnyOrComment; l.state != nil; {
		l.state = l.state(l)
	}
	close(l.items)
}

// state functions

var singleRuneToken = map[rune]ItemType{
	'|': ItemPipe,
	'(': ItemLParen, ')': ItemRParen,
	'[': ItemLBracket, ']': ItemRBracket,
	'{': ItemLBrace, '}': ItemRBrace,
	'$': ItemDollar, ';': ItemSemicolon, '&': ItemAmpersand,
}

// lexAny is the default state. It allows any token but comment.
func lexAny(l *Lexer) stateFn {
	var r rune
	switch r = l.next(); r {
	case eof:
		l.emit(ItemEOF, ItemTerminated)
		return nil
	case '>', '<':
		l.backup()
		return lexRedirLeader
	case '`':
		return lexSingleQuoted
	case '"':
		return lexDoubleQuoted
	case '\n':
		l.emit(ItemEndOfLine, ItemTerminated)
		return lexAnyOrComment
	case '?':
		// TODO
		switch l.next() {
		case '>':
			l.emit(ItemStatusRedirLeader, ItemTerminated)
			return lexAny
		case '(':
			l.emit(ItemQuestionLParen, ItemTerminated)
			return lexAny
		default:
			l.backup()
			return lexBare
		}
	}
	if isSigil(r) {
		r2 := l.peek()
		if TerminatesCompound(r2) {
			// Lone sigil; treat as bareword
			return lexBare
		}
		l.emit(ItemSigil, ItemTerminated)
		if isSigil(r2) {
			// Another sigil, parse as bareword
			return lexBare
		}
		return lexAny
	}
	if isSpace(r) {
		return lexSpace
	}
	if it, ok := singleRuneToken[r]; ok {
		l.emit(it, ItemTerminated)
		return lexAny
	}
	return lexBare
}

// lexAnyOrComment like lexAny, but allows comments.
func lexAnyOrComment(l *Lexer) stateFn {
	if l.peek() == '#' {
		return lexComment
	}
	return lexAny
}

// lexComment scans a (line) comment. It runs until the newline or eof.
// The leading hash has already been seen.
func lexComment(l *Lexer) stateFn {
loop:
	for {
		switch l.next() {
		case '\n', eof:
			l.backup()
			break loop
		}
	}
	l.emit(ItemSpace, ItemAmbiguous)
	return lexAny
}

// lexSpace scans a run of space characters.
// One space has already been seen.
func lexSpace(l *Lexer) stateFn {
	for isSpace(l.peek()) {
		l.next()
	}
	l.emit(ItemSpace, ItemAmbiguous)
	return lexAnyOrComment
}

// lexRedirLeader scans an IO redirection leader.
// It is started by one of < <> > >> >? and may be followed immediately by a
// string surrounded by square brackets. The internal structure of the string
// is not checked here.
func lexRedirLeader(l *Lexer) stateFn {
	switch r := l.next(); r {
	case '<', '>':
		if l.peek() == '>' {
			l.next()
		}
	default:
		panic("unreachable")
	}

	if l.peek() == '[' {
	loop:
		for {
			switch l.next() {
			case ']':
				l.emit(ItemRedirLeader, ItemTerminated)
				break loop
			case eof:
				l.emit(ItemRedirLeader, ItemUnterminated)
				break loop
			}
		}
	} else {
		l.emit(ItemRedirLeader, ItemAmbiguous)
	}

	return lexAny
}

// lexBare scans a bare string.
// The first rune has already been seen.
func lexBare(l *Lexer) stateFn {
	for !TerminatesBare(l.peek()) {
		l.next()
	}
	l.emit(ItemBare, ItemAmbiguous)
	return lexAny
}

// StartsBare determines whether r may be the first rune of a bareword.
//
// XXX(xiaq): StartsBare must be carefully maintained to match lexAny.
func StartsBare(r rune) bool {
	switch r {
	case '#', eof, '>', '<', '`', '"', '\n', '?':
		return false
	}
	if isSigil(r) || isSpace(r) {
		return false
	}
	if _, ok := singleRuneToken[r]; ok {
		return false
	}
	return true
}

// TerminatesCompound determines whether r terminates a compound expression.
// This is used to determine whether a sigil is "alone" and should be treated
// as a bareword instead.
//
// XXX(xiaq): This is bad abstraction, since the lexer now knows something
// about the the grammar. Perhaps there is a better way to do it.
func TerminatesCompound(r rune) bool {
	switch r {
	case '\n', ')', ']', '}', '<', '>', '?', ';', '|', eof:
		return true
	}
	return isSpace(r)
}

// TerminatesBare determines whether r terminates a bareword.
func TerminatesBare(r rune) bool {
	switch r {
	case '\n', '(', ')', '[', ']', '{', '}', '<', '>', '?',
		'"', '`', '$', ';', '|', eof:
		return true
	}
	return isSpace(r)
}

// lexSingleQuoted scans a single-quoted string.
// The opening quote has already been seen.
func lexSingleQuoted(l *Lexer) stateFn {
	const quote = '`'
loop:
	for {
		switch l.next() {
		case eof, '\n':
			l.emit(ItemSingleQuoted, ItemUnterminated)
			return lexAny
		case quote:
			if l.peek() != quote {
				break loop
			}
			l.next()
		}
	}
	l.emit(ItemSingleQuoted, ItemAmbiguous)
	return lexAny
}

// lexDoubleQuoted scans a double-quoted string.
// The opening quote has already been seen.
func lexDoubleQuoted(l *Lexer) stateFn {
loop:
	for {
		switch l.next() {
		case '\\':
			if r := l.next(); r != eof && r != '\n' {
				break
			}
			fallthrough
		case eof, '\n':
			l.emit(ItemDoubleQuoted, ItemUnterminated)
			return lexAny
		case '"':
			break loop
		}
	}
	l.emit(ItemDoubleQuoted, ItemTerminated)
	return lexAny
}

// isSpace reports whether r is a space character.
func isSpace(r rune) bool {
	return r == ' ' || r == '\t'
}

// isSigil determines whether r is a sigil.
func isSigil(r rune) bool {
	switch r {
	case '!', '%':
		return true
	default:
		return false
	}
}
