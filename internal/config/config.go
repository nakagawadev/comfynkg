// Package config loads, validates and edits /etc/comfynkg.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

const DefaultPath = "/etc/comfynkg.yaml"

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	rootRe     = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`) // goes into unit files: no newlines, %, quotes or spaces
	memoryRe   = regexp.MustCompile(`^(infinity|[0-9]+[KMGT]?)$`)
	hostRe     = regexp.MustCompile(`^([a-z_][a-z0-9_-]*@)?[A-Za-z0-9][A-Za-z0-9.-]*$`) // goes into an ssh command line
	ownedFlags = []string{"--listen", "--port", "--cuda-device", "--extra-model-paths-config"}
	Types      = []string{"image", "video"}
)

type Instance struct {
	Name      string `yaml:"-"`
	Type      string `yaml:"type"`
	Port      int    `yaml:"port"`
	GPU       *int   `yaml:"gpu"`
	Flags     string `yaml:"flags"`
	MemoryMax string `yaml:"memory_max"`
	Enabled   bool   `yaml:"enabled"`
}

type SyncType struct {
	Source string   `yaml:"source"`
	To     []string `yaml:"to"`
}

type Owner struct {
	Name     string
	UID, GID int
	Home     string
}

type Config struct {
	Path      string
	Host      string
	Root      string
	Flags     string
	MemoryMax string
	Models    map[string]string
	Plugin    map[string]any
	HasPlugin bool // a `plugin:` key exists, even empty
	Sync      map[string]SyncType
	Hosts     []string    // other servers `status` also shows: host or user@host
	Instances []*Instance // sorted by name
	Owner     Owner       // filled by Validate

	doc *yaml.Node
}

// Load reads and parses the file. It does not validate; call Validate.
// The file drives what root does, so it must belong to root and not be writable by anyone else.
func Load(path string) (*Config, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if uid, _, ok := fileOwner(fi); !ok || uid != 0 || fi.Mode().Perm()&0o022 != 0 {
		return nil, &ValidationError{Errs: []string{fmt.Sprintf("%s must be owned by root and not writable by group or others (chown root:root, chmod 644)", path)}}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.Path = path
	return c, nil
}

// Parse reads a config from YAML without validating it; Load also checks ownership and runs Validate.
func Parse(data []byte) (*Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	c := &Config{doc: &doc}
	if len(doc.Content) == 0 {
		return c, nil
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, errors.New("top level must be a mapping")
	}
	for i := 0; i+1 < len(top.Content); i += 2 {
		key, val := top.Content[i].Value, top.Content[i+1]
		var err error
		switch key {
		case "host":
			err = val.Decode(&c.Host)
		case "root":
			err = val.Decode(&c.Root)
		case "flags":
			err = val.Decode(&c.Flags)
		case "memory_max":
			err = val.Decode(&c.MemoryMax)
		case "models":
			err = strict(val, &c.Models)
		case "plugin":
			c.HasPlugin = true
			err = val.Decode(&c.Plugin)
		case "sync":
			err = strict(val, &c.Sync)
		case "hosts":
			err = val.Decode(&c.Hosts)
		default:
			if !nameRe.MatchString(key) {
				return nil, fmt.Errorf("line %d: %q is not a valid instance name (%s)", top.Content[i].Line, key, nameRe)
			}
			inst := &Instance{Name: key}
			if err = strict(val, inst); err == nil {
				c.Instances = append(c.Instances, inst)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	sort.Slice(c.Instances, func(i, j int) bool { return c.Instances[i].Name < c.Instances[j].Name })
	return c, nil
}

// strict decodes a node rejecting unknown keys.
func strict(n *yaml.Node, out any) error {
	b, err := yaml.Marshal(n)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	return dec.Decode(out)
}

// Instance returns the instance with that name, or nil.
func (c *Config) Instance(name string) *Instance {
	for _, in := range c.Instances {
		if in.Name == name {
			return in
		}
	}
	return nil
}

// Dir is the instance folder, {root}/{name}.
func (c *Config) Dir(in *Instance) string { return filepath.Join(c.Root, in.Name) }

// Python is the instance venv interpreter.
func (c *Config) Python(in *Instance) string {
	return filepath.Join(c.Dir(in), ".venv", "bin", "python")
}

// MemoryMaxFor returns the instance value, else the global one.
func (c *Config) MemoryMaxFor(in *Instance) string {
	if in.MemoryMax != "" {
		return in.MemoryMax
	}
	return c.MemoryMax
}

// ExtraFlags are the global flags followed by the instance flags.
func (c *Config) ExtraFlags(in *Instance) ([]string, error) {
	g, err := SplitArgs(c.Flags)
	if err != nil {
		return nil, fmt.Errorf("flags: %w", err)
	}
	f, err := SplitArgs(in.Flags)
	if err != nil {
		return nil, fmt.Errorf("%s.flags: %w", in.Name, err)
	}
	return append(g, f...), nil
}

// HostLabel is `host`, else the OS hostname up to the first dot.
func (c *Config) HostLabel() string {
	if c.Host != "" {
		return c.Host
	}
	h, _ := os.Hostname()
	h, _, _ = strings.Cut(h, ".")
	return h
}

// Validate checks the whole file. Errors stop the command; warnings are printed.
func (c *Config) Validate() (warnings []string, err error) {
	var errs []string
	bad := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if c.Root == "" || !filepath.IsAbs(c.Root) {
		bad("root: required, absolute path")
	} else if !rootRe.MatchString(c.Root) {
		bad("root: %q may only contain letters, digits and . _ - /", c.Root)
	} else if fi, statErr := os.Stat(c.Root); statErr != nil || !fi.IsDir() {
		bad("root: %s is not a directory", c.Root)
	} else if owner, oErr := OwnerOf(fi); oErr != nil {
		bad("root: %v", oErr)
	} else if owner.UID == 0 {
		bad("root: %s is owned by root; instances must belong to an unprivileged user", c.Root)
	} else {
		c.Owner = owner
		if dirs := DotDirsToCreate(owner.Home, owner.UID); len(dirs) > 0 {
			warnings = append(warnings, fmt.Sprintf("%s can't write in its home %s (a Hestia user?), and tools that keep files there fail at run time; create them: install -d -o %s -g %s -m 700 %s",
				owner.Name, owner.Home, owner.Name, owner.Name, strings.Join(dirs, " ")))
		}
	}
	if c.MemoryMax != "" && !memoryRe.MatchString(c.MemoryMax) {
		bad("memory_max: %q is not a systemd size (like 48G)", c.MemoryMax)
	}
	if _, e := SplitArgs(c.Flags); e != nil {
		bad("flags: %v", e)
	}
	for t := range c.Models {
		if !isType(t) {
			bad("models.%s: unknown type (image, video)", t)
		}
	}

	ports := map[int]string{}
	for _, in := range c.Instances {
		p := in.Name
		if !isType(in.Type) {
			bad("%s.type: must be image or video", p)
		} else if m := c.Models[in.Type]; m == "" {
			bad("%s: models.%s is not set", p, in.Type)
		} else if _, e := os.Stat(m); e != nil {
			bad("%s: models.%s: %v", p, in.Type, e)
		}
		if in.Port < 1024 || in.Port > 65535 {
			bad("%s.port: must be 1024-65535", p)
		} else if other, dup := ports[in.Port]; dup {
			bad("%s.port: %d already used by %s", p, in.Port, other)
		} else {
			ports[in.Port] = p
		}
		if in.GPU == nil || *in.GPU < 0 {
			bad("%s.gpu: required", p)
		}
		if in.MemoryMax != "" && !memoryRe.MatchString(in.MemoryMax) {
			bad("%s.memory_max: %q is not a systemd size", p, in.MemoryMax)
		}
		flags, e := c.ExtraFlags(in)
		if e != nil {
			bad("%v", e)
			continue
		}
		for _, f := range flags {
			for _, owned := range ownedFlags {
				if f == owned || strings.HasPrefix(f, owned+"=") {
					bad("%s: %s is set by comfynkg, remove it from flags", p, owned)
				}
			}
		}
		if !contains(flags, "--disable-metadata") {
			warnings = append(warnings, fmt.Sprintf("%s has no --disable-metadata", p))
		}
		if in.Enabled && c.Root != "" {
			if _, e := os.Stat(c.Dir(in)); e != nil {
				warnings = append(warnings, fmt.Sprintf("%s is enabled but %s does not exist (create it first)", p, c.Dir(in)))
			}
		}
	}
	errs = append(errs, c.validateSync()...)
	errs = append(errs, c.validatePlugin()...)
	for _, h := range c.Hosts {
		if !hostRe.MatchString(h) {
			bad("hosts: %q is not a host name or user@host", h)
		}
	}
	if len(errs) > 0 {
		return warnings, &ValidationError{Errs: errs}
	}
	return warnings, nil
}

type ValidationError struct{ Errs []string }

func (e *ValidationError) Error() string {
	return "invalid config:\n  " + strings.Join(e.Errs, "\n  ")
}

// OwnerOf returns the user that owns a file.
func OwnerOf(fi os.FileInfo) (Owner, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return Owner{}, errors.New("cannot read owner")
	}
	o := Owner{UID: int(st.Uid), GID: int(st.Gid)}
	u, err := user.LookupId(strconv.Itoa(o.UID))
	if err != nil {
		return o, fmt.Errorf("owner uid %d: %w", o.UID, err)
	}
	o.Name, o.Home = u.Username, u.HomeDir
	return o, nil
}

func isType(t string) bool { return contains(Types, t) }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// fileOwner returns the uid and gid of a file (ok is false off Unix).
// homeDotDirs are the folders uv, comfy-cli, pip, Hugging Face, Ultralytics (.cache, .config, .local),
// Triton (.triton) and the CUDA cache (.nv) create in the owner's home.
var homeDotDirs = []string{".cache", ".config", ".local", ".triton", ".nv"}

// DotDirsToCreate lists the usual dot folders the owner (uid) can't create itself: none when it owns its
// home, otherwise the ones that are missing or not its own. On Hestia the home belongs to root.
func DotDirsToCreate(home string, uid int) []string {
	fi, err := os.Stat(home)
	if err != nil {
		return nil
	}
	if huid, _, ok := fileOwner(fi); !ok || huid == uid || fi.Mode().Perm()&0o002 != 0 {
		return nil
	}
	var out []string
	for _, d := range homeDotDirs {
		p := filepath.Join(home, d)
		if fi, err := os.Lstat(p); err == nil && fi.IsDir() {
			if duid, _, ok := fileOwner(fi); ok && duid == uid {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

func fileOwner(fi os.FileInfo) (uid, gid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(st.Uid), int(st.Gid), true
}

// validateSync checks the sync section: known types, an absolute source, at least one destination,
// and no models file inside a source (it would overwrite the workers' own).
func (c *Config) validateSync() []string {
	var errs []string
	for t, s := range c.Sync {
		if !isType(t) {
			errs = append(errs, fmt.Sprintf("sync.%s: unknown type (image, video)", t))
			continue
		}
		if s.Source == "" || !filepath.IsAbs(s.Source) {
			errs = append(errs, fmt.Sprintf("sync.%s.source: required, absolute path", t))
			continue
		}
		if len(s.To) == 0 {
			errs = append(errs, fmt.Sprintf("sync.%s.to: at least one destination", t))
		}
		for mt, m := range c.Models {
			if rel, err := filepath.Rel(s.Source, m); err == nil && !strings.HasPrefix(rel, "..") {
				errs = append(errs, fmt.Sprintf("models.%s is inside sync.%s.source; it would overwrite the workers' file", mt, t))
			}
		}
	}
	return errs
}

// validatePlugin checks the plugin section: only stats and instances, and instances that exist.
func (c *Config) validatePlugin() []string {
	var errs []string
	for k := range c.Plugin {
		if k != "stats" && k != "instances" {
			errs = append(errs, fmt.Sprintf("plugin.%s: unknown key (stats, instances)", k))
		}
	}
	insts, _ := c.Plugin["instances"].(map[string]any)
	for name := range insts {
		if c.Instance(name) == nil {
			errs = append(errs, fmt.Sprintf("plugin.instances.%s: no such instance", name))
		}
	}
	return errs
}

// PluginFile renders comfyui_nakagawa.yaml for one model type:
// plugin.stats, drain always on, per-instance overrides keyed by port.
// Types that share a models file share this file, so it holds their instances too.
func (c *Config) PluginFile(typ string) ([]byte, error) {
	out := map[string]any{"drain": true}
	if st, ok := c.Plugin["stats"]; ok {
		out["stats"] = st
	}
	insts, _ := c.Plugin["instances"].(map[string]any)
	byPort := map[string]any{}
	for name, v := range insts {
		in := c.Instance(name)
		if in == nil || c.PluginPath(in.Type) != c.PluginPath(typ) {
			continue
		}
		m, _ := v.(map[string]any)
		flat := map[string]any{}
		for k, val := range m {
			if sub, ok := val.(map[string]any); ok && k == "stats" {
				for sk, sv := range sub {
					flat[sk] = sv
				}
			} else {
				flat[k] = val
			}
		}
		byPort[strconv.Itoa(in.Port)] = flat
	}
	if len(byPort) > 0 {
		out["instances"] = byPort
	}
	body, err := yaml.Marshal(out)
	if err != nil {
		return nil, err
	}
	return append([]byte("# managed by comfynkg - edit /etc/comfynkg.yaml\n"), body...), nil
}

// PluginPath is where the plugin looks for its config: next to the models file.
func (c *Config) PluginPath(typ string) string {
	return filepath.Join(filepath.Dir(c.Models[typ]), "comfyui_nakagawa.yaml")
}
