package shared

import "testing"

func TestApplicationIdentity_WithMetadata_NeverBlanksExistingFields(t *testing.T) {
	// Arrange
	id := NewApplicationIdentity("Zoom.exe", `C:\Users\libra\AppData\Roaming\Zoom\bin\Zoom.exe`)
	id = id.WithMetadata("Zoom Video Communications", "Zoom", "", "Zoom Meetings", "")

	// Act — a later call arrives with some fields missing (the common case:
	// metadata extraction only runs once per session per path).
	id = id.WithMetadata("", "", "Zoom.exe", "", "abc123")

	// Assert
	if id.Company != "Zoom Video Communications" {
		t.Errorf("Company = %q, want unchanged", id.Company)
	}
	if id.ProductName != "Zoom" {
		t.Errorf("ProductName = %q, want unchanged", id.ProductName)
	}
	if id.FileDescription != "Zoom Meetings" {
		t.Errorf("FileDescription = %q, want unchanged", id.FileDescription)
	}
	if id.OriginalFilename != "Zoom.exe" {
		t.Errorf("OriginalFilename = %q, want newly set", id.OriginalFilename)
	}
	if id.SHA256 != "abc123" {
		t.Errorf("SHA256 = %q, want newly set", id.SHA256)
	}
}

func TestApplicationIdentity_SameApp(t *testing.T) {
	tests := []struct {
		name string
		a, b ApplicationIdentity
		want bool
	}{
		{
			name: "same sha256 wins regardless of other fields",
			a:    ApplicationIdentity{SHA256: "abc", ExeName: "a.exe"},
			b:    ApplicationIdentity{SHA256: "abc", ExeName: "b.exe"},
			want: true,
		},
		{
			name: "different sha256 never matches even with same exe name",
			a:    ApplicationIdentity{SHA256: "abc", ExeName: "same.exe"},
			b:    ApplicationIdentity{SHA256: "def", ExeName: "same.exe"},
			want: false,
		},
		{
			name: "product+company case-insensitive match",
			a:    ApplicationIdentity{ProductName: "Zoom", Company: "Zoom Video Communications"},
			b:    ApplicationIdentity{ProductName: "zoom", Company: "ZOOM VIDEO COMMUNICATIONS"},
			want: true,
		},
		{
			name: "falls back to exe name when nothing richer is known",
			a:    ApplicationIdentity{ExeName: "notepad.exe"},
			b:    ApplicationIdentity{ExeName: "NOTEPAD.EXE"},
			want: true,
		},
		{
			name: "empty exe name never matches",
			a:    ApplicationIdentity{},
			b:    ApplicationIdentity{},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			got := tc.a.SameApp(tc.b)

			// Assert
			if got != tc.want {
				t.Errorf("SameApp() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApplicationIdentity_Key_NeverPanicsOnEmptyStruct(t *testing.T) {
	// Arrange
	id := ApplicationIdentity{}

	// Act / Assert — must not panic, deterministic empty-string result is fine.
	if got := id.Key(); got != "" {
		t.Errorf("Key() on empty struct = %q, want empty string", got)
	}
}
