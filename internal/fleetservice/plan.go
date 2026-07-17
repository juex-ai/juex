package fleetservice

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const defaultFleetAddr = "127.0.0.1:8080"

var systemdBareArgument = regexp.MustCompile(`^[A-Za-z0-9_./:@-]+$`)

func buildPlan(opts Options, host hostInfo) (registrationPlan, error) {
	if opts.Addr == "" {
		opts.Addr = defaultFleetAddr
	}
	if err := validateInstalledAddress(opts.Addr, opts.UnsafeBindAny); err != nil {
		return registrationPlan{}, err
	}
	home, err := absolutePath("fleet home", opts.HomeDir)
	if err != nil {
		return registrationPlan{}, err
	}
	executable, err := absolutePath("executable", opts.Executable)
	if err != nil {
		return registrationPlan{}, err
	}
	if err := validateRenderedValue("address", opts.Addr); err != nil {
		return registrationPlan{}, err
	}
	identity := serviceIdentity(home)
	args := []string{"fleet", "serve", "--addr", opts.Addr}
	if opts.UnsafeBindAny {
		args = append(args, "--unsafe-bind-any")
	}

	switch host.goos {
	case "darwin":
		return buildLaunchdPlan(home, executable, args, identity, host)
	case "linux":
		if host.termuxPrefix != "" {
			return buildTermuxPlan(home, executable, args, identity, host)
		}
		return buildSystemdPlan(home, executable, args, identity, host)
	case "windows":
		return registrationPlan{}, fmt.Errorf("fleet service: automatic service registration is not supported on Windows")
	default:
		return registrationPlan{}, fmt.Errorf("fleet service: automatic service registration is not supported on %s", host.goos)
	}
}

func validateInstalledAddress(addr string, unsafeBindAny bool) error {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("fleet service: address must be a host:port TCP address (got %q)", addr)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		if port == 0 && err == nil {
			return fmt.Errorf("fleet service: installed address must use a non-zero port")
		}
		return fmt.Errorf("fleet service: address port must be between 1 and 65535")
	}
	if host == "" {
		return fmt.Errorf("fleet service: address host is required")
	}
	if !unsafeBindAny && !isLoopbackHost(host) {
		return fmt.Errorf("fleet service: non-loopback address %q requires UnsafeBindAny", addr)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func absolutePath(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("fleet service: %s is required", label)
	}
	if err := validateRenderedValue(label, value); err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", fmt.Errorf("fleet service: resolve %s: %w", label, err)
	}
	return abs, nil
}

func validateRenderedValue(label, value string) error {
	for _, r := range value {
		if r == 0 || r == '\n' || r == '\r' {
			return fmt.Errorf("fleet service: %s contains an unsupported control character", label)
		}
	}
	return nil
}

func serviceIdentity(home string) string {
	base := strings.TrimLeft(filepath.Base(home), ".")
	var slug strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(base) {
		if unicode.IsLetter(r) && r <= unicode.MaxASCII || unicode.IsDigit(r) && r <= unicode.MaxASCII {
			slug.WriteRune(r)
			lastDash = false
		} else if slug.Len() > 0 && !lastDash {
			slug.WriteByte('-')
			lastDash = true
		}
		if slug.Len() >= 24 {
			break
		}
	}
	name := strings.Trim(slug.String(), "-")
	if name == "" {
		name = "home"
	}
	digest := sha256.Sum256([]byte(filepath.Clean(home)))
	return name + "-" + hex.EncodeToString(digest[:6])
}

func buildLaunchdPlan(home, executable string, args []string, identity string, host hostInfo) (registrationPlan, error) {
	if host.uid < 0 {
		return registrationPlan{}, fmt.Errorf("fleet service: invalid launchd uid %d", host.uid)
	}
	userHome, err := absolutePath("user home", host.userHome)
	if err != nil {
		return registrationPlan{}, err
	}
	label := "ai.juex.fleet." + strings.ReplaceAll(identity, "-", ".")
	definitionPath := filepath.Join(userHome, "Library", "LaunchAgents", label+".plist")
	logPath := filepath.Join(home, "logs", "fleet-service.log")
	programArguments := append([]string{executable}, args...)
	body, err := renderLaunchd(label, programArguments, home, logPath)
	if err != nil {
		return registrationPlan{}, err
	}
	domain := fmt.Sprintf("gui/%d", host.uid)
	return registrationPlan{
		registration:  Registration{Platform: PlatformLaunchd, Name: label, DefinitionPath: definitionPath},
		files:         []definitionFile{{path: definitionPath, data: body, mode: 0o600}},
		launchdDomain: domain,
		launchdTarget: domain + "/" + label,
		launchdLogDir: filepath.Dir(logPath),
	}, nil
}

func renderLaunchd(label string, args []string, home, logPath string) ([]byte, error) {
	var body bytes.Buffer
	body.WriteString(xml.Header)
	body.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	encoder := xml.NewEncoder(&body)
	encoder.Indent("", "  ")
	start := func(name string) error { return encoder.EncodeToken(xml.StartElement{Name: xml.Name{Local: name}}) }
	end := func(name string) error { return encoder.EncodeToken(xml.EndElement{Name: xml.Name{Local: name}}) }
	textElement := func(name, value string) error {
		if err := start(name); err != nil {
			return err
		}
		if err := encoder.EncodeToken(xml.CharData(value)); err != nil {
			return err
		}
		return end(name)
	}
	key := func(value string) error { return textElement("key", value) }
	boolean := func(value bool) error {
		if value {
			if err := start("true"); err != nil {
				return err
			}
			return end("true")
		}
		if err := start("false"); err != nil {
			return err
		}
		return end("false")
	}
	if err := encoder.EncodeToken(xml.StartElement{
		Name: xml.Name{Local: "plist"},
		Attr: []xml.Attr{{Name: xml.Name{Local: "version"}, Value: "1.0"}},
	}); err != nil {
		return nil, err
	}
	if err := start("dict"); err != nil {
		return nil, err
	}
	entries := []struct {
		name  string
		value string
	}{
		{"Label", label},
		{"ProcessType", "Background"},
		{"StandardOutPath", logPath},
		{"StandardErrorPath", logPath},
	}
	if err := key("Label"); err != nil {
		return nil, err
	}
	if err := textElement("string", label); err != nil {
		return nil, err
	}
	if err := key("ProgramArguments"); err != nil {
		return nil, err
	}
	if err := start("array"); err != nil {
		return nil, err
	}
	for _, arg := range args {
		if err := textElement("string", arg); err != nil {
			return nil, err
		}
	}
	if err := end("array"); err != nil {
		return nil, err
	}
	if err := key("EnvironmentVariables"); err != nil {
		return nil, err
	}
	if err := start("dict"); err != nil {
		return nil, err
	}
	if err := key("JUEX_HOME"); err != nil {
		return nil, err
	}
	if err := textElement("string", home); err != nil {
		return nil, err
	}
	if err := end("dict"); err != nil {
		return nil, err
	}
	for _, boolEntry := range []struct {
		name  string
		value bool
	}{{"RunAtLoad", true}, {"KeepAlive", true}, {"AbandonProcessGroup", true}} {
		if err := key(boolEntry.name); err != nil {
			return nil, err
		}
		if err := boolean(boolEntry.value); err != nil {
			return nil, err
		}
	}
	for _, entry := range entries[1:] {
		if err := key(entry.name); err != nil {
			return nil, err
		}
		if err := textElement("string", entry.value); err != nil {
			return nil, err
		}
	}
	if err := end("dict"); err != nil {
		return nil, err
	}
	if err := end("plist"); err != nil {
		return nil, err
	}
	if err := encoder.Flush(); err != nil {
		return nil, err
	}
	data := bytes.ReplaceAll(body.Bytes(), []byte("<true></true>"), []byte("<true/>"))
	data = bytes.ReplaceAll(data, []byte("<false></false>"), []byte("<false/>"))
	data = append(data, '\n')
	return data, nil
}

func buildSystemdPlan(home, executable string, args []string, identity string, host hostInfo) (registrationPlan, error) {
	userHome, err := absolutePath("user home", host.userHome)
	if err != nil {
		return registrationPlan{}, err
	}
	configHome := strings.TrimSpace(host.xdgConfigHome)
	if configHome == "" {
		configHome = filepath.Join(userHome, ".config")
	} else {
		configHome, err = absolutePath("XDG_CONFIG_HOME", configHome)
		if err != nil {
			return registrationPlan{}, err
		}
	}
	unit := "juex-fleet-" + identity + ".service"
	definitionPath := filepath.Join(configHome, "systemd", "user", unit)
	allArgs := append([]string{executable}, args...)
	quoted := make([]string, 0, len(allArgs))
	for _, arg := range allArgs {
		quoted = append(quoted, systemdExecArgument(arg))
	}
	body := strings.Join([]string{
		"[Unit]",
		"Description=JueX fleet supervisor for " + strings.ReplaceAll(home, "%", "%%"),
		"",
		"[Service]",
		"Type=exec",
		"ExecStart=" + strings.Join(quoted, " "),
		"Environment=" + systemdEnvironmentAssignment("JUEX_HOME", home),
		"Restart=on-failure",
		"RestartSec=5",
		"KillMode=process",
		"",
		"[Install]",
		"WantedBy=default.target",
		"",
	}, "\n")
	return registrationPlan{
		registration: Registration{
			Platform:       PlatformSystemd,
			Name:           unit,
			DefinitionPath: definitionPath,
			Notes: []string{
				`For boot before login, enable user lingering separately: loginctl enable-linger "$USER"`,
			},
		},
		files:       []definitionFile{{path: definitionPath, data: []byte(body), mode: 0o600}},
		systemdUnit: unit,
	}, nil
}

func systemdExecArgument(value string) string {
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.ReplaceAll(value, "$", "$$")
	return systemdQuote(value)
}

func systemdEnvironmentAssignment(name, value string) string {
	value = strings.ReplaceAll(value, "%", "%%")
	return systemdQuote(name + "=" + value)
}

func systemdQuote(value string) string {
	if systemdBareArgument.MatchString(value) {
		return value
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\t", `\t`)
	return `"` + value + `"`
}

func buildTermuxPlan(home, executable string, args []string, identity string, host hostInfo) (registrationPlan, error) {
	prefix, err := absolutePath("Termux prefix", host.termuxPrefix)
	if err != nil {
		return registrationPlan{}, err
	}
	name := "juex-fleet-" + identity
	serviceDir := filepath.Join(prefix, "var", "service", name)
	runPath := filepath.Join(serviceDir, "run")
	logRunPath := filepath.Join(serviceDir, "log", "run")
	allArgs := append([]string{executable}, args...)
	quoted := make([]string, 0, len(allArgs))
	for _, arg := range allArgs {
		quoted = append(quoted, shellQuote(arg))
	}
	run := strings.Join([]string{
		"#!" + filepath.Join(prefix, "bin", "sh"),
		"export JUEX_HOME=" + shellQuote(home),
		"exec " + strings.Join(quoted, " "),
		"",
	}, "\n")
	logRun := strings.Join([]string{
		"#!" + filepath.Join(prefix, "bin", "sh"),
		"exec " + shellQuote(filepath.Join(prefix, "share", "termux-services", "svlogger")),
		"",
	}, "\n")
	return registrationPlan{
		registration: Registration{
			Platform:       PlatformTermux,
			Name:           name,
			DefinitionPath: runPath,
			Notes: []string{
				"termux-services must be installed and its service daemon initialized.",
				"Device reboot startup additionally requires Termux:Boot to source $PREFIX/etc/profile.d/start-services.sh.",
			},
		},
		files: []definitionFile{
			{path: runPath, data: []byte(run), mode: 0o700},
			{path: logRunPath, data: []byte(logRun), mode: 0o700},
		},
		termuxPrefix: prefix,
		termuxDir:    serviceDir,
	}, nil
}

func shellQuote(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `'"'"'`) + `'`
}
