package fleetservice

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExistingDefinitionArgsPreserveServeOptionsAcrossPlatforms(t *testing.T) {
	tests := []struct {
		name     string
		platform Platform
		body     string
		want     InstalledServeOptions
	}{
		{
			name:     "launchd legacy address and unsafe flag",
			platform: PlatformLaunchd,
			body: `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>ProgramArguments</key><array>
<string>/Applications/JueX/juex</string>
<string>fleet</string><string>serve</string>
<string>--addr</string><string>0.0.0.0:8181</string>
<string>--unsafe-bind-any</string>
</array>
</dict></plist>`,
			want: InstalledServeOptions{Addr: "0.0.0.0:8181", UnsafeBindAny: true},
		},
		{
			name:     "systemd legacy custom loopback",
			platform: PlatformSystemd,
			body: `[Service]
ExecStart="/home/test/JueX Bin/juex" fleet serve --addr 127.0.0.1:8182
`,
			want: InstalledServeOptions{Addr: "127.0.0.1:8182"},
		},
		{
			name:     "Termux legacy address and unsafe flag",
			platform: PlatformTermux,
			body: `#!/data/data/com.termux/files/usr/bin/sh
exec '/data/data/com.termux/files/home/JueX Bin/juex' 'fleet' 'serve' '--addr' '0.0.0.0:8183' '--unsafe-bind-any'
`,
			want: InstalledServeOptions{Addr: "0.0.0.0:8183", UnsafeBindAny: true},
		},
		{
			name:     "current definition retains unsafe flag without address",
			platform: PlatformSystemd,
			body:     "ExecStart=/home/test/juex fleet serve --unsafe-bind-any\n",
			want:     InstalledServeOptions{UnsafeBindAny: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args, err := existingDefinitionArgs(tt.platform, []byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			got, found, err := parseInstalledServeOptions(args)
			if err != nil {
				t.Fatal(err)
			}
			if !found || got != tt.want {
				t.Fatalf("options = %+v, found=%t; want %+v, true; args=%v", got, found, tt.want, args)
			}
		})
	}
}

func TestParseInstalledServeOptionsRejectsAmbiguousAddress(t *testing.T) {
	for _, args := range [][]string{
		{"juex", "fleet", "serve", "--addr"},
		{"juex", "fleet", "serve", "--addr", "127.0.0.1:8181", "--addr", "127.0.0.1:8182"},
		{"juex", "fleet", "serve", "--addr="},
	} {
		if _, _, err := parseInstalledServeOptions(args); err == nil {
			t.Fatalf("parseInstalledServeOptions(%v) succeeded", args)
		}
	}
}

func TestExistingServeOptionsUsesCurrentDefinitionPath(t *testing.T) {
	host := hostInfo{goos: "darwin", userHome: t.TempDir(), uid: 501}
	manager, err := newManagerForHost(testOptions(t.TempDir()), host, &fakeRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if got, found, err := manager.ExistingServeOptions(); err != nil || found || got != (InstalledServeOptions{}) {
		t.Fatalf("missing definition options = %+v, found=%t, error=%v", got, found, err)
	}

	path := manager.plan.registration.DefinitionPath
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("<plist><dict></dict></plist>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.ExistingServeOptions(); err == nil {
		t.Fatal("unrecognized existing definition was silently accepted")
	}
}
