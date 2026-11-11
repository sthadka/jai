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

// MergeGeneratedViews adds generated (in-memory) views without overwriting any
// user-defined view of the same name: user views always win, and a colliding
// generated name is skipped and returned for visibility. Generated views are
// never written back to the config file, so a metrics-config change can never
// leave a stale, apparently-valid generated view on disk. Returns the names
// added and the names skipped due to a user collision.
func (c *Config) MergeGeneratedViews(gen []ViewConfig) (added, collisions []string) {
	for _, v := range gen {
		if c.ViewByName(v.Name) != nil {
			collisions = append(collisions, v.Name)
			continue
		}
		c.Views = append(c.Views, v)
		added = append(added, v.Name)
	}
	return added, collisions
}
