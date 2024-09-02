package chainmap

// Dict is a simple string->string map.
type Dict map[string]string

// ChainMap contains a slice of Dicts for interpolation values.
type ChainMap struct {
	maps []Dict
}

// New creates a new ChainMap.
func New(dicts ...Dict) *ChainMap {
	chainMap := &ChainMap{
		maps: make([]Dict, 0),
	}
	chainMap.maps = append(chainMap.maps, dicts...)

	return chainMap
}

// Add adds given dicts to the ChainMap.
func (c *ChainMap) Add(dicts ...Dict) {
	c.maps = append(c.maps, dicts...)
}

// Len returns the ammount of Dicts in the ChainMap.
func (c *ChainMap) Len() int {
	return len(c.maps)
}

// Get gets the last value with the given key from the ChainMap.
// If key does not exist returns empty string.
func (c *ChainMap) Get(key string) string {
	var value string

	for _, dict := range c.maps {
		if result, present := dict[key]; present {
			value = result
		}
	}
	return value
}
