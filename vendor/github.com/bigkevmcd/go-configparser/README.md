# go-configparser [![Go](https://github.com/bigkevmcd/go-configparser/actions/workflows/go.yml/badge.svg)](https://github.com/bigkevmcd/go-configparser/actions/workflows/go.yml)
Go implementation of the Python ConfigParser class.

This can parse Python-compatible ConfigParser config files, including support for option interpolation.

## Setup
```Go
  import (
    "github.com/bigkevmcd/go-configparser"
  )
```

## Parsing configuration files
It's easy to parse a configuration file.
```Go
  p, err := configparser.NewConfigParserFromFile("example.cfg")
  if err != nil {
    ...
  }
```

## Methods
The ConfigParser implements most of the Python ConfigParser API
```Go
  v, err := p.Get("section", "option")
  err = p.Set("section", "newoption", "value")

  s := p.Sections()
```

## Interpolation
The ConfigParser implements interpolation in the same format as the Python implementation.

Given the configuration

```
  [DEFAULTS]
  dir: testing

  [testing]
  something: %(dir)s/whatever
```

```Go
  v, err := p.GetInterpolated("testing, something")
```

It's also possible to override the values to use when interpolating values by providing a Dict to lookup values in.
```
  d := make(configparser.Dict)
  d["dir"] = "/a/non/existent/path"
  result, err := p.GetInterpolatedWithVars("testing", "something", d)
```

Will get ```testing/whatever``` as the value

## Options
The ConfigParser supports almost all custom options available in the Python version.

* Delimiters - allows to set custom **key-value** pair delimiters.
* CommentPrefixes - allows to set custom comment line prefix. If line starts with one of the given `Prefixes` it will be passed during parsing.
* InlineCommentPrefixes - allows to set custom inline comment delimiter. This option checks if the line contains any of the given `Prefixes` and if so, splits the string by the prefix and returns the 0 index of the slice.
* MultilinePrefixes - allows to set custom multiline values prefixes. This option checks if the line starts with one of the given `Prefixes` and if so, counts it as a part of the current value.
* Strict - if set to `true`, parser will return new wrapped `ErrAlreadyExist` for duplicates of *sections* or *options* in one source.
* AllowEmptyLines - if set to `true` allows multiline values to include empty lines as their part. Otherwise the value will be parsed until an empty line or the line which does not start with one of the allowed multiline prefixes.
* Interpolation - allows to set custom behaviour for values interpolation. Interface was added, which defaults to `chainmap.ChainMap` instance.
```go
type Interpolator interface {
	Add(...chainmap.Dict)
	Len() int
	Get(string) string
}
```
* Converters - allows to set custom values parsers.
```go
type ConvertFunc func(string) (any, error)
```
`ConvertFunc` can modify requested value if needed e.g.,
```go
package main

import (
	"fmt"
	"strings"

	"github.com/bigkevmcd/go-configparser"
)

func main() {
	stringConv := func(s string) (any, error) {
		return s + "_updated", nil
	}

	conv := configparser.Converter{
		configparser.String: stringConv,
	}

	p, err := configparser.ParseReaderWithOptions(
		strings.NewReader("[section]\noption=value\n\n"),
		configparser.Converters(conv),
	)
	// handle err

	v, err := p.Get("section", "option")
	// handle err

	fmt.Println(v == "value_updated") // true
}
```
Those functions triggered inside `ConfigParser.Get*` methods if presented and wraps the return value. 
> NOTE: Since `ConvertFunc` returns `any`, the caller should guarantee type assertion to the requested type after custom processing!
```go
type Converter map[string]ConvertFunc
```
`Converter` is a `map` type, which supports *int* (for `int64`), *string*, *bool*, *float* (for `float64`) keys.

---
Default options, which are always preset:
```go
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
```
