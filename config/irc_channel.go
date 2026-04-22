package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// IRChannel is one configured IRC channel, optionally with a channel key (+k "password").
// AutoJoin: nil or true = join on connect / treat as "autoinjoin" for live apply. False = kept in
// config (e.g. key reference, RSS) but the bot will not auto-join that channel.
type IRChannel struct {
	Name     string `yaml:"name" json:"name"`
	Password string `yaml:"password,omitempty" json:"-"` // never expose in web/API JSON
	AutoJoin *bool  `yaml:"auto_join,omitempty" json:"auto_join,omitempty"`
}

// IRChannelNames returns channel names in order (for list diff, logging, etc.).
func IRChannelNames(chs []IRChannel) []string {
	out := make([]string, len(chs))
	for i, c := range chs {
		out[i] = c.Name
	}
	return out
}

// AutoJoinEnabled reports whether this entry should be auto-joined on connect (default: true if unset).
func (c IRChannel) AutoJoinEnabled() bool {
	if c.AutoJoin == nil {
		return true
	}
	return *c.AutoJoin
}

// IRChannelNamesAutoJoin returns channel names that have auto-join enabled (for JOIN/PART diffs and startup).
func IRChannelNamesAutoJoin(chs []IRChannel) []string {
	var out []string
	for _, c := range chs {
		if c.AutoJoinEnabled() {
			out = append(out, c.Name)
		}
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
			AutoJoin *bool  `yaml:"auto_join"`
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
		c.AutoJoin = m.AutoJoin
		if c.Name == "" {
			return fmt.Errorf("irc channel: mapping needs name or channel")
		}
		return nil
	default:
		return fmt.Errorf("irc channel: expected string or map, got yaml kind %d", n.Kind)
	}
}

// MarshalYAML writes a plain string when there is no key and autoinjoin is on; otherwise a small map.
func (c IRChannel) MarshalYAML() (interface{}, error) {
	if c.Password == "" && c.AutoJoinEnabled() {
		return c.Name, nil
	}
	m := map[string]interface{}{
		"name": c.Name,
	}
	if c.Password != "" {
		m["password"] = c.Password
	}
	if c.AutoJoin != nil && !*c.AutoJoin {
		m["auto_join"] = false
	}
	return m, nil
}
