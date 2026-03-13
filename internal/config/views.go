package config

// ViewByName finds a view config by name. Returns nil if not found.
func (c *Config) ViewByName(name string) *ViewConfig {
	for i := range c.Views {
		if c.Views[i].Name == name {
			return &c.Views[i]
		}
	}
	return nil
}
