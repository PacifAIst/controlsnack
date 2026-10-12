// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// LoadOptions says where configuration comes from beyond the defaults.
type LoadOptions struct {
	// ConfigFile is the --config flag. Empty selects DefaultPath, which is allowed to be absent; an explicit
	// file must exist.
	ConfigFile string
	// Flags is the parsed flag set of the running command. Every flag listed in FlagKeys that exists in it is
	// bound, and only flags the user actually set override the other sources.
	Flags *pflag.FlagSet
	// Unsafe is the value of --unsafe. It is applied after loading and never read from file or environment.
	Unsafe bool
	// Getenv supplies the environment layer and defaults to os.Getenv; UserConfigDir defaults to os.UserConfigDir.
	Getenv        func(string) string
	UserConfigDir func() (string, error)
}

// Loaded is the result of Load.
type Loaded struct {
	Config Config
	// File is the config file that was read; empty when none exists.
	File string
	// Explicit lists the flags the user set on the command line (names without dashes).
	Explicit map[string]bool
	// Warnings are non-fatal findings, e.g. a world-readable file that holds an API key.
	Warnings []string
}

// DefaultPath is where the config file lives when --config is not given: <user config dir>/controlsnack/config.yaml.
func DefaultPath(userConfigDir func() (string, error)) (string, error) {
	if userConfigDir == nil {
		userConfigDir = os.UserConfigDir
	}
	dir, err := userConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate the user config directory: %w", err)
	}
	return filepath.Join(dir, "controlsnack", "config.yaml"), nil
}

// Defaults returns the built-in configuration, before any file, environment or flag is applied.
func Defaults() Config {
	v := viper.New()
	applyDefaults(v)
	var c Config
	// The defaults table is validated by tests against the struct, so a decode failure is a programming error.
	if err := v.Unmarshal(&c, decodeOption()); err != nil {
		panic(fmt.Sprintf("config: defaults do not decode: %v", err))
	}
	return c
}

func applyDefaults(v *viper.Viper) {
	for key, val := range defaultValues {
		v.SetDefault(key, val)
	}
}

func decodeOption() viper.DecoderConfigOption {
	return viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
		mapstructure.StringToTimeDurationHookFunc(),
		splitCommaLists(),
		mapstructure.TextUnmarshallerHookFunc(),
	))
}

// splitCommaLists turns "a, b" from an environment variable into a list for any slice target. The stock
// mapstructure hook only handles []string; for []int it would wrap the whole string into one element.
func splitCommaLists() mapstructure.DecodeHookFunc {
	return func(from, to reflect.Type, data any) (any, error) {
		if from.Kind() != reflect.String || to.Kind() != reflect.Slice {
			return data, nil
		}
		raw := strings.TrimSpace(reflect.ValueOf(data).String())
		if raw == "" {
			return []string{}, nil
		}
		parts := strings.Split(raw, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts, nil
	}
}

// Load builds the effective configuration. Precedence: flags > environment > config file > defaults.
func Load(opts LoadOptions) (*Loaded, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	v := viper.New()
	applyDefaults(v)

	out := &Loaded{Explicit: map[string]bool{}}

	if getenv(EnvPrefix+"_SAFETY_UNSAFE") != "" {
		return nil, fmt.Errorf("%s_SAFETY_UNSAFE is ignored on purpose: unsafe mode can only be requested with the --unsafe flag", EnvPrefix)
	}

	path := opts.ConfigFile
	explicit := path != ""
	if !explicit {
		p, err := DefaultPath(opts.UserConfigDir)
		if err != nil {
			return nil, err
		}
		path = p
	}
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	switch err := v.ReadInConfig(); {
	case err == nil:
		out.File = path
	case !explicit && errors.Is(err, fs.ErrNotExist):
		// No config file is the normal case.
	default:
		return nil, fmt.Errorf("config file %s: %w", path, err)
	}
	if out.File != "" && v.InConfig("safety.unsafe") {
		return nil, fmt.Errorf("config file %s: safety.unsafe is not a config key; unsafe mode can only be requested with the --unsafe flag", path)
	}

	// The environment layer is read through getenv rather than by viper, so it is injectable and tests are
	// hermetic. Merging it after the file puts it above the file; flags are bound below and stay on top.
	if layer := envLayer(getenv); len(layer) > 0 {
		if err := v.MergeConfigMap(layer); err != nil {
			return nil, fmt.Errorf("environment: %w", err)
		}
	}

	if opts.Flags != nil {
		for name, key := range FlagKeys {
			f := opts.Flags.Lookup(name)
			if f == nil {
				continue
			}
			if err := v.BindPFlag(key, f); err != nil {
				return nil, fmt.Errorf("binding --%s: %w", name, err)
			}
			if f.Changed {
				out.Explicit[name] = true
			}
		}
	}

	if err := v.UnmarshalExact(&out.Config, decodeOption()); err != nil {
		return nil, fmt.Errorf("invalid configuration value: %s", unwrapDecodeError(err))
	}
	out.Config.Safety.Unsafe = opts.Unsafe

	out.Warnings = out.collectWarnings()
	return out, nil
}

// unwrapDecodeError trims mapstructure's wording down to the part a user can act on.
func unwrapDecodeError(err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "decoding failed due to the following error(s):\n\n")
	return strings.ReplaceAll(strings.TrimSpace(msg), "\n", "; ")
}

func (l *Loaded) collectWarnings() []string {
	var warns []string
	if l.File != "" && l.Config.LLM.APIKey != "" && runtime.GOOS != "windows" {
		if info, err := os.Stat(l.File); err == nil && info.Mode().Perm()&0o077 != 0 {
			warns = append(warns, fmt.Sprintf("%s holds llm.api_key but is readable by other users (mode %04o); run: chmod 600 %s",
				l.File, info.Mode().Perm(), l.File))
		}
	}
	if !l.Config.Net.Allow && (len(l.Config.Net.AllowDomains) > 0 || len(l.Config.Net.AllowPorts) > 0) {
		warns = append(warns, "net.allow_domains / net.allow_ports are set but guest networking is off; they only apply together with --allow-net")
	}
	return warns
}

// EnvName returns the environment variable that sets a config key: vm.memory_mib is CONTROLSNACK_VM_MEMORY_MIB.
func EnvName(key string) string {
	return EnvPrefix + "_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// envLayer collects every CONTROLSNACK_* variable that names a config key into a nested map. Empty values count
// as unset.
func envLayer(getenv func(string) string) map[string]any {
	layer := map[string]any{}
	for key := range defaultValues {
		val := getenv(EnvName(key))
		if val == "" {
			continue
		}
		parts := strings.Split(key, ".")
		m := layer
		for _, p := range parts[:len(parts)-1] {
			next, ok := m[p].(map[string]any)
			if !ok {
				next = map[string]any{}
				m[p] = next
			}
			m = next
		}
		m[parts[len(parts)-1]] = val
	}
	return layer
}
