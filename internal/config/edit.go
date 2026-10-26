package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Settable keys and their YAML tag.
var instanceKeys = map[string]string{"type": "!!str", "port": "!!int", "gpu": "!!int", "flags": "!!str", "memory_max": "!!str", "enabled": "!!bool"}
var globalKeys = map[string]string{"host": "!!str", "flags": "!!str", "memory_max": "!!str"}

// Set changes one key of an instance (or of the top level when name is "global")
// in the YAML tree, keeping comments. Call Bytes to get the new file.
func (c *Config) Set(name, key, value string) error {
	keys := instanceKeys
	if name == "global" {
		keys = globalKeys
	}
	tag, ok := keys[key]
	if !ok {
		return fmt.Errorf("%s: unknown key %q", name, key)
	}
	switch tag {
	case "!!int":
		if _, err := strconv.Atoi(value); err != nil {
			return fmt.Errorf("%s: %q is not a number", key, value)
		}
	case "!!bool":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("%s: %q is not true/false", key, value)
		}
		value = strconv.FormatBool(b)
	}
	if len(c.doc.Content) == 0 {
		return fmt.Errorf("empty config")
	}
	m := c.doc.Content[0]
	if name != "global" {
		if m = mapValue(m, name); m == nil {
			return fmt.Errorf("instance %q not found", name)
		}
	}
	if v := mapValue(m, key); v != nil {
		v.Kind, v.Tag, v.Value, v.Style = yaml.ScalarNode, tag, value, 0
		return nil
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
	return nil
}

// mapValue returns the value node of key in a YAML mapping, or nil. Editing nodes in place keeps
// the file's comments and key order.
func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// Bytes renders the (possibly edited) YAML tree.
func (c *Config) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c.doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

// WriteFile replaces path atomically, keeping its mode and owner.
func WriteFile(path string, data []byte) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".comfynkg-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), fi.Mode().Perm()); err != nil {
		return err
	}
	if uid, gid, ok := fileOwner(fi); ok {
		_ = os.Lchown(tmp.Name(), uid, gid) // only works as root; the file keeps the runner's owner otherwise
	}
	return os.Rename(tmp.Name(), path)
}

// AddInstance appends a new instance block (disabled) to the YAML tree.
func (c *Config) AddInstance(name, typ string, port, gpu int) error {
	if len(c.doc.Content) == 0 {
		return fmt.Errorf("empty config")
	}
	top := c.doc.Content[0]
	if mapValue(top, name) != nil {
		return fmt.Errorf("instance %q already exists", name)
	}
	str := func(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }
	val := func(tag, v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: v} }
	block := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		str("type"), str(typ),
		str("port"), val("!!int", strconv.Itoa(port)),
		str("gpu"), val("!!int", strconv.Itoa(gpu)),
		str("enabled"), val("!!bool", "false"),
	}}
	top.Content = append(top.Content, str(name), block)
	return nil
}

// RemoveInstance deletes an instance block from the YAML tree.
func (c *Config) RemoveInstance(name string) error {
	if len(c.doc.Content) == 0 {
		return fmt.Errorf("empty config")
	}
	top := c.doc.Content[0]
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value == name {
			top.Content = append(top.Content[:i], top.Content[i+2:]...)
			return nil
		}
	}
	return fmt.Errorf("instance %q not found", name)
}
