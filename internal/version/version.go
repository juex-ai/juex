// Package version exposes build metadata + a small Info struct that can be
// rendered as text or JSON. Build metadata variables are populated at build
// time via -ldflags -X; defaults match an unstamped `go build`.
package version

import (
	"encoding/json"
	"fmt"
	"runtime"
)

var (
	Version   = "0.0.1-dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

// Info bundles build metadata together with runtime context that the CLI
// layer fills in. Only inputs that cannot be derived from each other show
// up here: paths like sessions_dir / memory_dir / home_agents are always
// `<work_dir>/.juex/{sessions,memory}` and `~/.agents` respectively, so
// they are intentionally omitted to keep the surface tight.
type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`

	// Runtime context (optional). Each field is independent input — none
	// of them is derivable from the others.
	WorkDir      string `json:"work_dir,omitempty"`
	ConfigFile   string `json:"config_file,omitempty"`
	ProviderType string `json:"provider_type,omitempty"`
	Model        string `json:"model,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
}

// Build returns an Info populated only with build metadata. CLI layer adds
// runtime fields on top.
func Build() Info {
	return Info{
		Name:      "juex",
		Version:   Version,
		Commit:    Commit,
		BuildTime: BuildTime,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

// String returns the short single-line form ("juex <version>").
func String() string {
	return fmt.Sprintf("juex %s", Version)
}

// Verbose returns the multi-line human-readable form. Empty optional fields
// are skipped.
func (i Info) Verbose() string {
	out := fmt.Sprintf("juex %s\n  commit:        %s\n  built:         %s\n  go:            %s\n  os/arch:       %s/%s",
		i.Version, i.Commit, i.BuildTime, i.GoVersion, i.OS, i.Arch)
	if i.WorkDir != "" {
		out += "\n  work_dir:      " + i.WorkDir
	}
	if i.ConfigFile != "" {
		out += "\n  config_file:   " + i.ConfigFile
	}
	if i.ProviderType != "" {
		out += "\n  provider_type: " + i.ProviderType
	}
	if i.Model != "" {
		out += "\n  model:         " + i.Model
	}
	if i.BaseURL != "" {
		out += "\n  base_url:      " + i.BaseURL
	}
	return out
}

// JSON returns the info as a pretty-printed JSON document.
func (i Info) JSON() string {
	b, _ := json.MarshalIndent(i, "", "  ")
	return string(b)
}

// Verbose is a convenience for callers that don't need runtime context.
func Verbose() string {
	return Build().Verbose()
}
