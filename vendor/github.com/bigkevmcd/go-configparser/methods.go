package configparser

import (
	"fmt"
	"sort"
	"strconv"
)

func (p *ConfigParser) isDefaultSection(section string) bool {
	return section == p.opt.defaultSection
}

// Defaults returns the items in the map used for default values.
func (p *ConfigParser) Defaults() Dict {
	return p.defaults.Items()
}

// Sections returns a list of section names, excluding [DEFAULT].
func (p *ConfigParser) Sections() []string {
	sections := make([]string, 0)
	for section := range p.config {
		sections = append(sections, section)
	}
	sort.Strings(sections)

	return sections
}

// AddSection creates a new section in the configuration.
//
// Returns an error if a section by the specified name
// already exists.
// Returns an error if the specified name DEFAULT or any of its
// case-insensitive variants.
// Returns nil if no error and the section is created
func (p *ConfigParser) AddSection(section string) error {
	if p.isDefaultSection(section) {
		return fmt.Errorf("invalid section name: %q", section)
	} else if p.HasSection(section) {
		return fmt.Errorf("section %q already exists", section)
	}
	p.config[section] = newSection(section)

	return nil
}

// HasSection returns true if the named section is present in the
// configuration.
//
// The DEFAULT section is not acknowledged.
func (p *ConfigParser) HasSection(section string) bool {
	_, present := p.config[section]

	return present
}

// Options returns a list of option mames for the given section name.
//
// Returns an error if the section does not exist.
func (p *ConfigParser) Options(section string) ([]string, error) {
	if !p.HasSection(section) {
		return nil, getNoSectionError(section)
	}
	seenOptions := make(map[string]bool)
	for _, option := range p.config[section].Options() {
		seenOptions[option] = true
	}
	for _, option := range p.defaults.Options() {
		seenOptions[option] = true
	}
	options := make([]string, 0)
	for option := range seenOptions {
		options = append(options, option)
	}
	sort.Strings(options)

	return options, nil
}

// Get returns string value for the named option.
//
// Returns an error if a section does not exist.
// Returns an error if the option does not exist either in the section or in
// the defaults.
func (p *ConfigParser) Get(section, option string) (string, error) {
	result, err := p.get(section, option)
	if err != nil {
		return "", err
	}

	value, err := p.opt.converters[StringConv](result)
	if err != nil {
		return "", err
	}

	return value.(string), nil
}

func (p *ConfigParser) get(section, option string) (string, error) {
	if !p.HasSection(section) {
		if !p.isDefaultSection(section) {
			return "", getNoSectionError(section)
		}
		if value, err := p.defaults.Get(option); err != nil {
			return "", getNoOptionError(section, option)
		} else {
			return value, nil
		}
	} else if value, err := p.config[section].Get(option); err == nil {
		return value, nil
	} else if value, err := p.defaults.Get(option); err == nil {
		return value, nil
	}

	return "", getNoOptionError(section, option)
}

// ItemsWithDefaults returns a copy of the named section Dict including
// any values from the Defaults.
//
// NOTE: This is different from the Python version which returns a list of
// tuples
func (p *ConfigParser) ItemsWithDefaults(section string) (Dict, error) {
	if !p.HasSection(section) {
		return nil, getNoSectionError(section)
	}
	s := make(Dict)

	for k, v := range p.defaults.Items() {
		s[k] = v
	}
	for k, v := range p.config[section].Items() {
		s[k] = v
	}

	return s, nil
}

// Items returns a copy of the section Dict not including the Defaults.
//
// NOTE: This is different from the Python version which returns a list of
// tuples.
func (p *ConfigParser) Items(section string) (Dict, error) {
	if section == p.opt.defaultSection {
		return p.defaults.Items(), nil
	}

	if !p.HasSection(section) {
		return nil, getNoSectionError(section)
	}

	return p.config[section].Items(), nil
}

// Set puts the given option into the named section.
//
// Returns an error if the section does not exist.
func (p *ConfigParser) Set(section, option, value string) error {
	var setSection *Section

	if p.isDefaultSection(section) {
		setSection = p.defaults
	} else if _, present := p.config[section]; !present {
		return getNoSectionError(section)
	} else {
		setSection = p.config[section]
	}

	return setSection.Add(option, value)
}

// GetInt64 returns int64 representation of the named option.
//
// Returns an error if a section does not exist.
// Returns an error if the option does not exist either in the section or in
// the defaults.
func (p *ConfigParser) GetInt64(section, option string) (int64, error) {
	result, err := p.get(section, option)
	if err != nil {
		return 0, err
	}

	value, err := p.opt.converters[IntConv](result)
	if err != nil {
		return 0, err
	}

	return value.(int64), nil
}

// GetFloat64 returns float64 representation of the named option.
//
// Returns an error if a section does not exist.
// Returns an error if the option does not exist either in the section or in
// the defaults.
func (p *ConfigParser) GetFloat64(section, option string) (float64, error) {
	result, err := p.get(section, option)
	if err != nil {
		return 0, err
	}

	value, err := p.opt.converters[FloatConv](result)
	if err != nil {
		return 0, err
	}

	return value.(float64), nil
}

// GetBool returns bool representation of the named option.
//
// Returns an error if a section does not exist.
// Returns an error if the option does not exist either in the section or in
// the defaults.
func (p *ConfigParser) GetBool(section, option string) (bool, error) {
	result, err := p.get(section, option)
	if err != nil {
		return false, err
	}

	value, err := p.opt.converters[BoolConv](result)
	if err != nil {
		return false, err
	}

	return value.(bool), nil
}

// RemoveSection removes given section from the ConfigParser.
func (p *ConfigParser) RemoveSection(section string) error {
	if !p.HasSection(section) {
		return getNoSectionError(section)
	}
	delete(p.config, section)

	return nil
}

// HasOption checks if section contains option.
func (p *ConfigParser) HasOption(section, option string) (bool, error) {
	var s *Section
	if p.isDefaultSection(section) {
		s = p.defaults
	} else if _, present := p.config[section]; !present {
		return false, getNoSectionError(section)
	} else {
		s = p.config[section]
	}
	_, err := s.Get(option)

	return err == nil, nil
}

// RemoveOption removes option from the section.
func (p *ConfigParser) RemoveOption(section, option string) error {
	var s *Section
	if p.isDefaultSection(section) {
		s = p.defaults
	} else if _, present := p.config[section]; !present {
		return getNoSectionError(section)
	} else {
		s = p.config[section]
	}

	return s.Remove(option)
}

func (p *ConfigParser) inOptions(key string) error {
	opts, err := p.allOptions()
	if err != nil {
		return err
	}

	for _, o := range opts {
		if key == o {
			return fmt.Errorf(
				"option %q already exists and strict flag was set",
				key,
			)
		}
	}

	return nil
}

func (p *ConfigParser) allOptions() ([]string, error) {
	sections := p.Sections()
	options := make([]string, 0)
	for _, s := range sections {
		o, err := p.Options(s)
		if err != nil {
			return nil, err
		}

		options = append(options, o...)
	}

	return options, nil
}

func defaultGet(value string) (any, error) { return value, nil }

func defaultGetInt64(value string) (any, error) {
	return strconv.ParseInt(value, 10, 64)
}

func defaultGetFloat64(value string) (any, error) {
	return strconv.ParseFloat(value, 64)
}

func defaultGetBool(value string) (any, error) {
	booleanValue, present := boolMapping[value]
	if !present {
		return false, fmt.Errorf("not a boolean: %q", value)
	}

	return booleanValue, nil
}
