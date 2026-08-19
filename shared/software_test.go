package shared

import "testing"

func TestSoftwareIdentity_Key(t *testing.T) {
	tests := []struct {
		name string
		a, b SoftwareIdentity
		want string // "same" or "different"
	}{
		{
			name: "1: same MSI ProductCode, different DisplayName -> same identity",
			a:    SoftwareIdentity{WindowsInstaller: true, ProductCode: "{A1B2C3D4-1234-5678-9ABC-1234567890AB}", DisplayName: "Foo 1.0"},
			b:    SoftwareIdentity{WindowsInstaller: true, ProductCode: "{A1B2C3D4-1234-5678-9ABC-1234567890AB}", DisplayName: "Foo 2.0"},
			want: "same",
		},
		{
			name: "2: same name+publisher, different version field is not part of identity -> same identity",
			a:    SoftwareIdentity{DisplayName: "Notepad++", Publisher: "Don Ho"},
			b:    SoftwareIdentity{DisplayName: "Notepad++", Publisher: "Don Ho"},
			want: "same",
		},
		{
			name: "3: same name, different publisher -> different identity",
			a:    SoftwareIdentity{DisplayName: "Update Tool", Publisher: "Vendor A"},
			b:    SoftwareIdentity{DisplayName: "Update Tool", Publisher: "Vendor B"},
			want: "different",
		},
		{
			name: "4: different names, same publisher -> different identity",
			a:    SoftwareIdentity{DisplayName: "App One", Publisher: "Acme"},
			b:    SoftwareIdentity{DisplayName: "App Two", Publisher: "Acme"},
			want: "different",
		},
		{
			name: "5: missing publisher -> deterministic identity",
			a:    SoftwareIdentity{DisplayName: "Solo App", Publisher: ""},
			b:    SoftwareIdentity{DisplayName: "Solo App", Publisher: ""},
			want: "same",
		},
		{
			name: "6: casing/leading/trailing whitespace differences -> same normalized identity",
			a:    SoftwareIdentity{DisplayName: "  Google Chrome ", Publisher: "GOOGLE LLC"},
			b:    SoftwareIdentity{DisplayName: "google chrome", Publisher: "  Google LLC  "},
			want: "same",
		},
		{
			name: "7: MSI ProductCode with braces/case differences -> same normalized identity",
			a:    SoftwareIdentity{WindowsInstaller: true, ProductCode: "{a1b2c3d4-1234-5678-9abc-1234567890ab}"},
			b:    SoftwareIdentity{WindowsInstaller: true, ProductCode: "A1B2C3D4-1234-5678-9ABC-1234567890AB"},
			want: "same",
		},
		{
			name: "8: different MSI ProductCodes -> different identity",
			a:    SoftwareIdentity{WindowsInstaller: true, ProductCode: "{A1B2C3D4-1234-5678-9ABC-1234567890AB}"},
			b:    SoftwareIdentity{WindowsInstaller: true, ProductCode: "{B2C3D4E5-1234-5678-9ABC-1234567890AB}"},
			want: "different",
		},
		{
			name: "9: non-MSI app and MSI app sharing display name/publisher -> different identity (disjoint key namespaces)",
			a:    SoftwareIdentity{WindowsInstaller: false, DisplayName: "Shared Name", Publisher: "Shared Pub"},
			b:    SoftwareIdentity{WindowsInstaller: true, ProductCode: "{A1B2C3D4-1234-5678-9ABC-1234567890AB}", DisplayName: "Shared Name", Publisher: "Shared Pub"},
			want: "different",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ka, kb := tt.a.Key(), tt.b.Key()
			if tt.want == "same" && ka != kb {
				t.Errorf("Key() = %q vs %q, want same", ka, kb)
			}
			if tt.want == "different" && ka == kb {
				t.Errorf("Key() = %q vs %q, want different", ka, kb)
			}
		})
	}
}

func TestSoftwareIdentity_Key_MSIPrefix(t *testing.T) {
	id := SoftwareIdentity{WindowsInstaller: true, ProductCode: "{a1b2c3d4-1234-5678-9abc-1234567890ab}"}
	got := id.Key()
	want := "msi:{A1B2C3D4-1234-5678-9ABC-1234567890AB}"
	if got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
}

func TestSoftwareIdentity_Key_NonMSIPrefix(t *testing.T) {
	id := SoftwareIdentity{DisplayName: "My App", Publisher: "My Pub"}
	got := id.Key()
	want := "np:my app|my pub"
	if got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
}

func TestSoftwareIdentity_Key_InvalidProductCodeFallsBackToNonMSI(t *testing.T) {
	// WindowsInstaller is set but ProductCode is malformed/empty — must not
	// panic or silently produce an "msi:" key with garbage in it.
	id := SoftwareIdentity{WindowsInstaller: true, ProductCode: "not-a-guid", DisplayName: "Foo", Publisher: "Bar"}
	got := id.Key()
	if got != "np:foo|bar" {
		t.Errorf("Key() = %q, want fallback np: key", got)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		name              string
		installed, avail  string
		wantOK            bool
		wantCmp           int // only checked when wantOK
	}{
		{"equal versions", "1.2.3", "1.2.3", true, 0},
		{"available is newer patch", "1.2.3", "1.2.4", true, -1},
		{"installed is newer than available", "2.0.0", "1.9.9", true, 1},
		{"formatting differences still equal", "v1.2.3", "1.2.3.0", true, 0},
		{"missing trailing components treated as zero", "1.2", "1.2.0", true, 0},
		{"available newer major version", "9.1", "10.0", true, -1},
		{"neither side has digits -> not comparable", "Continuous", "Stable", false, 0},
		{"installed has no digits -> not comparable", "Latest", "25.1.0", false, 0},
		{"empty installed -> not comparable", "", "1.0.0", false, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmp, ok := CompareVersions(tt.installed, tt.avail)
			if ok != tt.wantOK {
				t.Fatalf("CompareVersions(%q,%q) ok = %v, want %v", tt.installed, tt.avail, ok, tt.wantOK)
			}
			if ok && cmp != tt.wantCmp {
				t.Errorf("CompareVersions(%q,%q) cmp = %d, want %d", tt.installed, tt.avail, cmp, tt.wantCmp)
			}
		})
	}
}

func TestParseQuietUninstallCommand(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantOK   bool
		wantExe  string
		wantArgs []string
	}{
		{
			name:     "quoted absolute path with args",
			raw:      `"C:\Program Files\App\uninstall.exe" /S /silent`,
			wantOK:   true,
			wantExe:  `C:\Program Files\App\uninstall.exe`,
			wantArgs: []string{"/S", "/silent"},
		},
		{
			name:     "unquoted absolute path with args",
			raw:      `C:\App\uninst.exe /VERYSILENT`,
			wantOK:   true,
			wantExe:  `C:\App\uninst.exe`,
			wantArgs: []string{"/VERYSILENT"},
		},
		{
			name:     "msiexec is explicitly allowed",
			raw:      `MsiExec.exe /X{A1B2C3D4-1234-5678-9ABC-1234567890AB} /qn`,
			wantOK:   false, // not an absolute drive-letter path, so rejected regardless
			wantExe:  "",
			wantArgs: nil,
		},
		{
			name:     "absolute msiexec path is allowed",
			raw:      `"C:\Windows\System32\MsiExec.exe" /X{A1B2C3D4-1234-5678-9ABC-1234567890AB} /qn`,
			wantOK:   true,
			wantExe:  `C:\Windows\System32\MsiExec.exe`,
			wantArgs: []string{"/X{A1B2C3D4-1234-5678-9ABC-1234567890AB}", "/qn"},
		},
		{name: "empty string rejected", raw: "", wantOK: false},
		{name: "NUL byte rejected", raw: "C:\\App\\u.exe\x00 /S", wantOK: false},
		{name: "relative path rejected", raw: `uninstall.exe /S`, wantOK: false},
		{name: "bare command name rejected", raw: `notepad`, wantOK: false},
		{name: "UNC path rejected", raw: `\\server\share\uninstall.exe`, wantOK: false},
		{name: "unbalanced quote rejected", raw: `"C:\App\uninstall.exe /S`, wantOK: false},
		{name: "non-exe extension rejected", raw: `C:\App\uninstall.bat`, wantOK: false},
		{name: "cmd.exe blocklisted", raw: `C:\Windows\System32\cmd.exe /c del *`, wantOK: false},
		{name: "powershell.exe blocklisted", raw: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe -Command x`, wantOK: false},
		{name: "rundll32.exe blocklisted", raw: `C:\Windows\System32\rundll32.exe evil.dll,Entry`, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exe, args, ok := ParseQuietUninstallCommand(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("ParseQuietUninstallCommand(%q) ok = %v, want %v (exe=%q args=%v)", tt.raw, ok, tt.wantOK, exe, args)
			}
			if !tt.wantOK {
				return
			}
			if exe != tt.wantExe {
				t.Errorf("exe = %q, want %q", exe, tt.wantExe)
			}
			if len(args) != len(tt.wantArgs) {
				t.Fatalf("args = %v, want %v", args, tt.wantArgs)
			}
			for i := range args {
				if args[i] != tt.wantArgs[i] {
					t.Errorf("args[%d] = %q, want %q", i, args[i], tt.wantArgs[i])
				}
			}
		})
	}
}
