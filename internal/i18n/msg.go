// msg.go
// Language-neutral messages and errors: a catalog key with its arguments, rendered in the viewer's language
// Version: 2026.10.28

package i18n

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Msg is one translatable message made at run time: a catalog key and its
// arguments, or (without a key) text from elsewhere that is shown as it is,
// such as the output of iperf3. Arguments are messages themselves, so a
// nested message ("Installation failed: winget is not available") is
// rendered in the viewer's language as a whole. A Msg is stored next to the
// English text of the message (see Localize).
type Msg struct {
	Key  string `json:"k,omitempty"`
	Args []Msg  `json:"a,omitempty"`
	Text string `json:"t,omitempty"`
}

// M builds a message for key. Arguments that are a Msg or an error stay
// translatable; every other argument is rendered with fmt.Sprint, so the
// catalog texts of runtime messages use %s only (TestRuntimeKeysUseStringVerbs).
func M(key string, args ...any) Msg {
	m := Msg{Key: key}
	for _, a := range args {
		m.Args = append(m.Args, argMsg(a))
	}
	return m
}

// Raw wraps text that has no translation.
func Raw(text string) Msg { return Msg{Text: text} }

func argMsg(a any) Msg {
	switch v := a.(type) {
	case Msg:
		return v
	case error:
		return MsgOf(v)
	default:
		return Raw(fmt.Sprint(v))
	}
}

// In renders the message in lang.
func (m Msg) In(lang string) string {
	if m.Key == "" {
		return m.Text
	}
	args := make([]any, len(m.Args))
	for i, a := range m.Args {
		args[i] = a.In(lang)
	}
	return T(lang, m.Key, args...)
}

// String renders the message in English, the language of logs and stored texts.
func (m Msg) String() string { return m.In(EN) }

// Known reports whether every key of the message is in the catalogs.
func (m Msg) Known() bool {
	if m.Key != "" {
		if _, ok := Lookup(EN, m.Key); !ok {
			return false
		}
	}
	for _, a := range m.Args {
		if !a.Known() {
			return false
		}
	}
	return true
}

// Encode returns the stored form of m (JSON), or "" for a message without a
// key: plain text needs no second column.
func (m Msg) Encode() string {
	if m.Key == "" {
		return ""
	}
	data, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(data)
}

// DecodeMsg reads a stored message; ok is false for an empty or unreadable value.
func DecodeMsg(s string) (m Msg, ok bool) {
	if s == "" || json.Unmarshal([]byte(s), &m) != nil || m.Key == "" {
		return Msg{}, false
	}
	return m, true
}

// Localize renders a stored message in lang. encoded is the stored Msg,
// english the English text stored with it. The Msg is used when every key is
// known to this version and it still renders to english (an older version
// that wrote only the English column leaves a stale Msg behind). Rows from
// versions before 2026.10.28 hold German text and no Msg; known German
// messages are recognized (see legacy.go). Anything else is shown as stored.
func Localize(lang, encoded, english string) string {
	if m, ok := storedMsg(encoded, english); ok {
		return m.In(lang)
	}
	return english
}

// StoredKey returns the catalog key of a stored message, as Localize renders
// it, or "" when it is shown as stored (text of another program, unknown key).
func StoredKey(encoded, english string) string {
	m, _ := storedMsg(encoded, english)
	return m.Key
}

// storedMsg returns the message a stored text stands for (see Localize).
func storedMsg(encoded, english string) (Msg, bool) {
	if m, ok := DecodeMsg(encoded); ok && m.Known() && m.String() == english {
		return m, true
	}
	return legacyMsg(english)
}

// LocalizePtr is Localize for an optional text; nil stays nil.
func LocalizePtr(lang, encoded string, english *string) *string {
	if english == nil {
		return nil
	}
	s := Localize(lang, encoded, *english)
	return &s
}

// Errors

// Error is an error whose message can be shown in the viewer's language.
// Error() returns the English text, so logs and callers that only know the
// error interface see English; the web UI renders it with ErrorIn.
type Error struct {
	Key  string
	Args []any
	// Cause, when set, is rendered as the last argument and returned by Unwrap.
	Cause error
}

// E returns an error with the message key and arguments.
func E(key string, args ...any) error { return &Error{Key: key, Args: args} }

// Wrap returns an error with the message key, the arguments and cause as the
// last argument; errors.Is and errors.As see cause.
func Wrap(cause error, key string, args ...any) error {
	return &Error{Key: key, Args: args, Cause: cause}
}

// Msg returns the error as a message.
func (e *Error) Msg() Msg {
	m := M(e.Key, e.Args...)
	if e.Cause != nil {
		m.Args = append(m.Args, MsgOf(e.Cause))
	}
	return m
}

// In renders the error in lang.
func (e *Error) In(lang string) string { return e.Msg().In(lang) }

// Error returns the English message.
func (e *Error) Error() string { return e.Msg().String() }

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Cause }

// Foreign returns an error with text from another program (iperf3,
// traceroute, the operating system). It has no translation and is shown as
// it is; using it marks that on purpose.
func Foreign(text string) error { return foreignError(text) }

type foreignError string

func (e foreignError) Error() string { return string(e) }

// MsgOf returns err as a message: the *Error in its chain when err adds no
// text of its own, else the text of err as it is (it comes from another
// program or library, or wraps a message with untranslated context).
func MsgOf(err error) Msg {
	if err == nil {
		return Msg{}
	}
	var e *Error
	if errors.As(err, &e) && (error(e) == err || e.Error() == err.Error()) {
		return e.Msg()
	}
	return Raw(err.Error())
}

// ErrorIn renders err in lang.
func ErrorIn(lang string, err error) string {
	if err == nil {
		return ""
	}
	return MsgOf(err).In(lang)
}

// KeyOf returns the catalog key of err, or "" when it has none.
func KeyOf(err error) string { return MsgOf(err).Key }
