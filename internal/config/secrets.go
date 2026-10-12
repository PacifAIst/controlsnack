// SPDX-FileCopyrightText: 2026 Dr. Manuel Herrador
// SPDX-License-Identifier: AGPL-3.0-only

package config

import "reflect"

// RedactedMark replaces secret values in anything that is printed or logged.
const RedactedMark = "***redacted***"

// Redacted returns a copy of c in which every field tagged secret:"true" is masked. Use it for `config show`, logs
// and the audit record of the effective configuration; never print a Config directly.
func (c Config) Redacted() Config {
	redactStruct(reflect.ValueOf(&c).Elem())
	return c
}

func redactStruct(v reflect.Value) {
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Struct:
			redactStruct(f)
		case reflect.String:
			if t.Field(i).Tag.Get("secret") == "true" && f.String() != "" {
				f.SetString(RedactedMark)
			}
		}
	}
}

// APIKey is a resolved API key and where it came from (the source is safe to show, the value is not).
type APIKey struct {
	Value  string
	Source string
}

// ResolveAPIKey finds the API key for the configured backend. Order (PLAN.md section 6.12):
//
//  1. the variable named by --api-key-env / llm.api_key_env, which is exclusive: if the user names a variable
//     it is the only place looked at;
//  2. CONTROLSNACK_LLM_API_KEY;
//  3. the backend's conventional variable (OPENAI_API_KEY, ANTHROPIC_API_KEY);
//  4. llm.api_key from the config file.
//
// getenv defaults to returning nothing, so a caller that forgets to pass one never reads the process environment
// by accident.
func ResolveAPIKey(c *Config, getenv func(string) string) (APIKey, bool) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	if name := c.LLM.APIKeyEnv; name != "" {
		if val := getenv(name); val != "" {
			return APIKey{Value: val, Source: "environment variable " + name}, true
		}
		return APIKey{}, false
	}
	candidates := []string{EnvPrefix + "_LLM_API_KEY"}
	switch c.LLM.Backend {
	case "openai":
		candidates = append(candidates, "OPENAI_API_KEY")
	case "anthropic":
		candidates = append(candidates, "ANTHROPIC_API_KEY")
	}
	for _, name := range candidates {
		if val := getenv(name); val != "" {
			return APIKey{Value: val, Source: "environment variable " + name}, true
		}
	}
	if c.LLM.APIKey != "" {
		return APIKey{Value: c.LLM.APIKey, Source: "config file"}, true
	}
	return APIKey{}, false
}
