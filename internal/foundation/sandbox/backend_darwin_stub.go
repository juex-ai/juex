//go:build !darwin

package sandbox

func prepareDarwin(lookPath func(string) (string, error), req Request) (ExecSpec, error) {
	return ExecSpec{}, NewError(ErrorCodeUnsupportedPlatform, "darwin", "sandbox-exec", "select", req.Policy, "The macOS sandbox backend is only available in darwin builds.", nil)
}
