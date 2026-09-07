package command

import (
	"runtime"
)

type ShellProfile struct {
	Profile       string
	Family        string
	Binary        string
	Args          []string
	PathStyle     string
	HostPathStyle string
}

func DefaultShellProfile() ShellProfile {
	if runtime.GOOS == "windows" {
		return ShellProfile{
			Profile:   "cmd",
			Family:    "cmd",
			Binary:    "cmd.exe",
			Args:      []string{"/c"},
			PathStyle: "windows",
		}
	}
	return ShellProfile{
		Profile:   "sh",
		Family:    "posix",
		Binary:    "sh",
		Args:      []string{"-c"},
		PathStyle: "posix",
	}
}
