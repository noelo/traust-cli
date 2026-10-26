package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Goose is the AAIF goose adapter, verified against goose 1.56: headless runs
// go through `goose run -i - --no-session` with the prompt on stdin, and the
// model comes from goose's own configuration. goose reads OPENAI_API_KEY
// from the ENVIRONMENT only — the value in its config.yaml is displayed by
// `goose info` but never plumbed into the provider — so this adapter
// bridges that quirk: when the env var is absent, the key is read from
// goose's config and injected into the child environment.
type Goose struct{}

func (g *Goose) ID() string          { return "goose" }
func (g *Goose) DisplayName() string { return "goose" }
func (g *Goose) Binary() string      { return binaryEnv("goose", "goose") }

func (g *Goose) Capabilities() Caps {
	return Caps{StreamJSON: false, ModelSelect: false, PermissionScoping: false, UsageReporting: false}
}

func (g *Goose) Detect(ctx context.Context) Detection { return detect(ctx, g.Binary()) }

func (g *Goose) Command(ctx context.Context, spec *RunSpec) *exec.Cmd {
	// TRAUST_CLI_GOOSE_ARGS overrides the argv template (space-separated,
	// {prompt} replaced by the composed prompt, {stdin} to take the prompt
	// on stdin). The default is the verified 1.56 headless invocation.
	template := os.Getenv("TRAUST_CLI_GOOSE_ARGS")
	if template == "" {
		template = "run -i - --no-session {stdin}"
	}
	var args []string
	stdinPrompt := false
	for _, field := range strings.Fields(template) {
		switch field {
		case "{prompt}":
			args = append(args, spec.Prompt)
		case "{stdin}":
			stdinPrompt = true
		default:
			args = append(args, field)
		}
	}
	cmd := exec.CommandContext(ctx, g.Binary(), args...)
	cmd.Dir = spec.Cwd
	cmd.Env = gooseEnv(spec.Env)
	if stdinPrompt {
		cmd.Stdin = strings.NewReader(spec.Prompt)
	}
	return cmd
}

// gooseEnv bridges goose's env-only API key: goose never reads the
// OPENAI_API_KEY sitting in its own config.yaml (it is displayed by
// `goose info` but not plumbed into the provider), and an ambient
// OPENAI_API_KEY belonging to some OTHER tool would shadow it. goose's
// config is therefore authoritative when present: its OPENAI_API_KEY (and
// OPENAI_HOST) replace any ambient values; without a config the ambient
// environment passes through untouched.
func gooseEnv(base []string) []string {
	out := base
	for _, key := range []string{"OPENAI_API_KEY", "OPENAI_HOST"} {
		val := gooseConfigValue(key)
		if val == "" {
			continue
		}
		out = replaceEnv(out, key, val)
	}
	return out
}

// replaceEnv sets key=value in an env slice, replacing any existing entry.
func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return append(out, key+"="+value)
}

// gooseConfigValue reads one key from goose's config.yaml.
func gooseConfigValue(key string) string {
	configDir := os.Getenv("GOOSE_CONFIG_DIR")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configDir = filepath.Join(home, ".config", "goose")
	}
	data, err := os.ReadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		return ""
	}
	var cfg map[string]string
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return strings.TrimSpace(cfg[key])
}

func (g *Goose) Notes() []string {
	return []string{
		"goose run emits plain text with an ASCII banner, not a structured stream: tool calls are not distinguished live",
		"token usage is not reported by goose run: the spend declaration is skipped",
		"model selection lives in goose's config (GOOSE_PROVIDER/GOOSE_MODEL in ~/.config/goose/config.yaml), not per invocation",
		"argv override: TRAUST_CLI_GOOSE_ARGS (default \"run -i - --no-session {stdin}\")",
	}
}

// ParseEvent always returns false: goose run output is unstructured text
// (banner, session header, reply).
func (g *Goose) ParseEvent(line []byte) (Event, bool) { return Event{}, false }
