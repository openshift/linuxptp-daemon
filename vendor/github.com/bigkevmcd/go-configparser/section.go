package configparser

import "strings"

// Section represent each section of the configuration file.
type Section struct {
	Name    string
	options Dict
	lookup  Dict
}

// Add adds new key-value pair to the section.
func (s *Section) Add(key, value string) error {
	lookupKey := s.safeKey(key)
	s.options[key] = s.safeValue(value)
	s.lookup[lookupKey] = key

	return nil
}

// Get returns value of an option with the given key.
//
// Returns an error if the option does not exist either in the section or in
// the defaults.
func (s *Section) Get(key string) (string, error) {
	lookupKey, present := s.lookup[s.safeKey(key)]
	if !present {
		return "", getNoOptionError(s.Name, key)
	}
	if value, present := s.options[lookupKey]; present {
		return value, nil
	}

	return "", getNoOptionError(s.Name, key)
}

// Options returns a slice of option names.
func (s *Section) Options() []string {
	return s.options.Keys()
}

// Items returns a Dict with the key-value pairs.
func (s *Section) Items() Dict {
	return s.options
}

func (s *Section) safeValue(in string) string {
	return strings.TrimSpace(in)
}

func (s *Section) safeKey(in string) string {
	return strings.ToLower(strings.TrimSpace(in))
}

// Remove removes option with the given name from the section.
//
// Returns an error if the option does not exist either in the section or in
// the defaults.
func (s *Section) Remove(key string) error {
	_, present := s.options[key]
	if !present {
		return getNoOptionError(s.Name, key)
	}

	// delete doesn't return anything, but this does require
	// that the passed key to be removed matches the options key.
	delete(s.lookup, s.safeKey(key))
	delete(s.options, key)

	return nil
}

func newSection(name string) *Section {
	return &Section{
		Name:    name,
		options: make(Dict),
		lookup:  make(Dict),
	}
}
