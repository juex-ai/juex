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

// Info records the immutable identity of a built binary.
type Info struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

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

// Verbose returns the build metadata in a human-readable form.
func (i Info) Verbose() string {
	return fmt.Sprintf("juex %s\n  commit:        %s\n  built:         %s\n  go:            %s\n  os/arch:       %s/%s", i.Version, i.Commit, i.BuildTime, i.GoVersion, i.OS, i.Arch)
}

// JSON returns the info as a pretty-printed JSON document.
func (i Info) JSON() string {
	b, _ := json.MarshalIndent(i, "", "  ")
	return string(b)
}

// Verbose formats the current build metadata.
func Verbose() string {
	return Build().Verbose()
}
