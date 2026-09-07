//go:build !linux

package sandbox

func prepareLinux(lookPath func(string) (string, error), req Request) (ExecSpec, error) {
	return ExecSpec{}, NewError(ErrorCodeUnsupportedPlatform, "linux", "bubblewrap", "select", req.Policy, "The Linux sandbox backend is only available in linux builds.", nil)
}
