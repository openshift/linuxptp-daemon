package configparser

import (
	"strings"

	"github.com/bigkevmcd/go-configparser/chainmap"
)

const defaultSectionName = "DEFAULT"

// options allows to control parser behavior.
type options struct {
	interpolation         Interpolator
	commentPrefixes       Prefixes
	inlineCommentPrefixes Prefixes
	multilinePrefixes     Prefixes
	defaultSection        string
	delimiters            string
	converters            Converter
	allowNoValue          bool
	emptyLines            bool
	strict                bool
}

// Converter contains custom convert functions for available types.
// The caller should guarantee type assertion to the requested type
// after custom processing!
type Converter map[int]ConvertFunc

// Predefined types for Converter.
const (
	StringConv = iota
	IntConv
	FloatConv
	BoolConv
)

// ConvertFunc is a custom datatype converter.
type ConvertFunc func(string) (any, error)

// Prefixes stores available prefixes for comments.
type Prefixes []string

// HasPrefix checks if str starts with any of the prefixes.
func (pr Prefixes) HasPrefix(str string) bool {
	for _, p := range pr {
		if strings.HasPrefix(str, p) {
			return true
		}
	}

	return false
}

// Split splits str with the first prefix found.
// Returns original string if no matches.
func (pr Prefixes) Split(str string) string {
	for _, p := range pr {
		if strings.Contains(str, p) {
			return strings.Split(str, p)[0]
		}
	}

	return str
}

// Interpolator defines interpolation instance.
// For more details, check [chainmap.ChainMap] realisation.
type Interpolator interface {
	Add(...chainmap.Dict)
	Len() int
	Get(string) string
}

// defaultOptions returns the struct of preset required options.
func defaultOptions() *options {
	return &options{
		interpolation:     chainmap.New(),
		defaultSection:    defaultSectionName,
		delimiters:        ":=",
		commentPrefixes:   Prefixes{"#", ";"},
		multilinePrefixes: Prefixes{"\t", " "},
		converters: Converter{
			StringConv: defaultGet,
			IntConv:    defaultGetInt64,
			FloatConv:  defaultGetFloat64,
			BoolConv:   defaultGetBool,
		},
	}
}

type optFunc func(*options)

// Interpolation sets custom interpolator.
func Interpolation(i Interpolator) optFunc {
	return func(o *options) {
		o.interpolation = i
	}
}

// CommentPrefixes sets a slice of comment prefixes.
// Lines of configuration file which starts with
// the first match in this slice will be skipped.
func CommentPrefixes(pr Prefixes) optFunc {
	return func(o *options) {
		o.commentPrefixes = pr
	}
}

// InlineCommentPrefixes sets a slice of inline comment delimiters.
// When parsing a value, it will be split with
// the first match in this slice.
func InlineCommentPrefixes(pr Prefixes) optFunc {
	return func(o *options) {
		o.inlineCommentPrefixes = pr
	}
}

// MultilinePrefixes sets a slice of prefixes, which will define
// multiline value.
func MultilinePrefixes(pr Prefixes) optFunc {
	return func(o *options) {
		o.multilinePrefixes = pr
	}
}

// DefaultSection sets the name of the default section.
func DefaultSection(n string) optFunc {
	return func(o *options) {
		o.defaultSection = n
	}
}

// Delimiters sets a string of delimiters for option-value pairs.
func Delimiters(d string) optFunc {
	return func(o *options) {
		o.delimiters = d
	}
}

// Converters sets custom convertion functions. Will apply to return
// value of the Get* methods instead of the default convertion.
//
// NOTE: the caller should guarantee type assetion to the requested type
// after custom processing or the method will panic.
func Converters(conv Converter) optFunc {
	return func(o *options) {
		o.converters = conv
	}
}

// AllowNoValue allows option with no value to be saved as empty line.
func AllowNoValue(o *options) { o.allowNoValue = true }

// Strict prohibits the duplicates of options and values.
func Strict(o *options) { o.strict = true }

// AllowEmptyLines allows empty lines in multiline values.
func AllowEmptyLines(o *options) { o.emptyLines = true }
