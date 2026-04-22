package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// IRChannel is one configured IRC channel, optionally with a channel key (+k "password").
type IRChannel struct {
	Name     string `yaml:"name" json:"name"`
	Password string `yaml:"password,omitempty" json:"-"` // never expose in web/API JSON
}

// IRChannelNames returns channel names in order (for list diff, logging, etc.).
func IRChannelNames(chs []IRChannel) []string {
	out := make([]string, len(chs))
	for i, c := range chs {
		out[i] = c.Name
	}
	return out
}

// FindIRChannelByName returns the config entry for a channel (case-insensitive) or (zero, false).
func FindIRChannelByName(chs []IRChannel, name string) (IRChannel, bool) {
	for _, c := range chs {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return IRChannel{}, false
}

// UnmarshalYAML accepts either a channel string (e.g. "#foo") or a mapping:
//
//	name: '##protected'
//	password: 'secretkey'
func (c *IRChannel) UnmarshalYAML(n *yaml.Node) error {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case yaml.ScalarNode:
		var s string
		if err := n.Decode(&s); err != nil {
			return err
		}
		c.Name = s
		c.Password = ""
		return nil
	case yaml.MappingNode:
		var m struct {
			Name     string `yaml:"name"`
			Channel  string `yaml:"channel"`
			Password string `yaml:"password"`
		}
		if err := n.Decode(&m); err != nil {
			return err
		}
		if m.Name != "" {
			c.Name = m.Name
		} else {
			c.Name = m.Channel
		}
		c.Password = m.Password
		if c.Name == "" {
			return fmt.Errorf("irc channel: mapping needs name or channel")
		}
		return nil
	default:
		return fmt.Errorf("irc channel: expected string or map, got yaml kind %d", n.Kind)
	}
}

// MarshalYAML writes a plain string when there is no key; otherwise a small map.
func (c IRChannel) MarshalYAML() (interface{}, error) {
	if c.Password == "" {
		return c.Name, nil
	}
	return map[string]string{
		"name":     c.Name,
		"password": c.Password,
	}, nil
}
