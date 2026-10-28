package config

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// EnvPrefix prefixes all environment variables.
const EnvPrefix = "IPERF3_"

// legacyEnv maps old unprefixed names to their key; still honoured with a
// log warning, the new name wins if both are set.
var legacyEnv = []struct{ name, key string }{
	{"LISTEN_ADDR", "web.listen"},
	{"DB_PATH", "storage.path"},
	{"SECRET_KEY", "auth.secret_key"},
	{"SCHEDULER_ENABLED", "scheduler.enabled"},
	{"IPERF3_PATH", "iperf.path"},
	{"IPERF3_AUTO_INSTALL", "iperf.auto_install"},
	{"GEOIP_PATH", "geoip.path"},
	{"LOG_LEVEL", "log.level"},
}

// EnvName returns the variable name for a key like "web.listen".
func EnvName(key string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// envField is a config value settable via the environment.
type envField struct {
	key string // "section.key"
	val reflect.Value
}

// envFields lists all config keys in file order.
func (c *Config) envFields() []envField {
	var out []envField
	root := reflect.ValueOf(c).Elem()
	for i := 0; i < root.NumField(); i++ {
		section, ok := yamlName(root.Type().Field(i))
		sv := root.Field(i)
		if !ok || sv.Kind() != reflect.Struct {
			continue
		}
		for j := 0; j < sv.NumField(); j++ {
			name, ok := yamlName(sv.Type().Field(j))
			if !ok {
				continue
			}
			out = append(out, envField{key: section + "." + name, val: sv.Field(j)})
		}
	}
	return out
}

func yamlName(f reflect.StructField) (string, bool) {
	tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
	if tag == "" || tag == "-" {
		return "", false
	}
	return tag, true
}

// EnvKeys returns all variable names (for docs and tests).
func EnvKeys() []string {
	var c Config
	var out []string
	for _, f := range c.envFields() {
		out = append(out, EnvName(f.key))
	}
	return out
}

// KeyInfo describes a config key for the product docs (i-tracker reference).
type KeyInfo struct {
	Key     string `json:"key"`
	Type    string `json:"type"`
	Default string `json:"default"`
	Env     string `json:"env"`
}

// Reference lists all config keys with their built-in defaults.
func Reference() []KeyInfo {
	var out []KeyInfo
	for _, f := range defaults().envFields() {
		out = append(out, KeyInfo{Key: f.key, Type: f.val.Kind().String(), Default: fmt.Sprint(f.val.Interface()), Env: EnvName(f.key)})
	}
	return out
}

// applyEnv applies the environment: deprecated aliases first, then the new
// names, which thus take precedence.
func (c *Config) applyEnv(lookup func(string) (string, bool)) error {
	fields := map[string]envField{}
	for _, f := range c.envFields() {
		fields[f.key] = f
	}
	for _, l := range legacyEnv {
		v, ok := lookup(l.name)
		if !ok || v == "" {
			continue
		}
		newName := EnvName(l.key)
		if nv, set := lookup(newName); set && nv != "" {
			c.Warnings = append(c.Warnings, fmt.Sprintf("Deprecated environment variable %s is ignored because %s is set", l.name, newName))
			continue
		}
		if err := setField(fields[l.key].val, v); err != nil {
			return fmt.Errorf("%s is invalid: %q", l.name, v)
		}
		c.Warnings = append(c.Warnings, fmt.Sprintf("Deprecated environment variable %s, please use %s", l.name, newName))
	}
	for _, f := range c.envFields() {
		name := EnvName(f.key)
		v, ok := lookup(name)
		if !ok || v == "" {
			continue
		}
		if err := setField(f.val, v); err != nil {
			return fmt.Errorf("%s is invalid: %q", name, v)
		}
	}
	return nil
}

func setField(v reflect.Value, s string) error {
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		v.SetInt(n)
	default:
		return fmt.Errorf("type %s not supported", v.Kind())
	}
	return nil
}
