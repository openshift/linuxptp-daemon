package configparser

import (
	"strings"

	"github.com/bigkevmcd/go-configparser/chainmap"
)

const maxInterpolationDepth int = 10

func (p *ConfigParser) getInterpolated(section, option string, c Interpolator) (string, error) {
	val, err := p.get(section, option)
	if err != nil {
		return "", err
	}
	return p.interpolate(val, c), nil
}

// GetInterpolated returns a string value for the named option.
//
// All % interpolations are expanded in the return values, based on
// the defaults passed into the constructor and the DEFAULT section.
func (p *ConfigParser) GetInterpolated(section, option string) (string, error) {
	o, err := p.Items(section)
	if err != nil {
		return "", err
	}
	p.opt.interpolation.Add(chainmap.Dict(p.Defaults()), chainmap.Dict(o))
	return p.getInterpolated(section, option, p.opt.interpolation)
}

// GetInterpolatedWithVars returns a string value for the named option.
//
// All % interpolations are expanded in the return values, based on the defaults passed
// into the constructor and the DEFAULT section.  Additional substitutions may be
// provided using the 'v' argument, which must be a Dict whose contents contents
// override any pre-existing defaults.
func (p *ConfigParser) GetInterpolatedWithVars(section, option string, v Dict) (string, error) {
	o, err := p.Items(section)
	if err != nil {
		return "", err
	}
	p.opt.interpolation.Add(chainmap.Dict(p.Defaults()), chainmap.Dict(o), chainmap.Dict(v))
	return p.getInterpolated(section, option, p.opt.interpolation)
}

// Private method which does the work of interpolating a value
// interpolates the value using the values in the ChainMap
// returns the interpolated string.
func (p *ConfigParser) interpolate(value string, options Interpolator) string {
	for i := 0; i < maxInterpolationDepth; i++ {
		if strings.Contains(value, "%(") {
			value = interpolater.ReplaceAllStringFunc(value, func(m string) string {
				// No ReplaceAllStringSubMatchFunc so apply the regexp twice
				match := interpolater.FindAllStringSubmatch(m, 1)[0][1]
				replacement := options.Get(match)
				return replacement
			})
		}
	}
	return value
}

// ItemsWithDefaultsInterpolated returns a copy of the dict for the section.
func (p *ConfigParser) ItemsWithDefaultsInterpolated(section string) (Dict, error) {
	s, err := p.ItemsWithDefaults(section)
	if err != nil {
		return nil, err
	}
	// TOOD: Optimise this...instantiate the ChainMap and delegate to interpolate()
	for k := range s {
		v, err := p.GetInterpolated(section, k)
		if err != nil {
			return nil, err
		}
		s[k] = v
	}
	return s, nil
}
