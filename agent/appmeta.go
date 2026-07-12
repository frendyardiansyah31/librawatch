package main

import (
	"os"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// AppMetadata carries the facts extractMetadataIfNew pulls from a single
// executable: PE version-info resource fields plus filesystem stat data.
// Extending this later with SHA256 / digital-signature fields only means
// adding to this struct and populating it here — nothing else in the
// pipeline (metrics.go, the server catalog) needs to change shape.
type AppMetadata struct {
	ProductName    string
	Company        string
	Description    string
	ProductVersion string
	Size           int64
	FileCreatedAt  time.Time
	FileModifiedAt time.Time
}

// metaExtracted caches which executable paths have already had their
// metadata extracted this agent session, so repeated sightings of the same
// process (every 30s, for the lifetime of the PC being on) don't repeatedly
// hit the filesystem and parse the PE version resource. The cache is
// intentionally in-memory only — it resets on agent restart, which just
// costs one extra extraction pass, not correctness.
var metaExtracted sync.Map // path (string) -> struct{}

// extractMetadataIfNew returns metadata for path the first time it's seen
// this session, and nil on every subsequent call for the same path (even if
// extraction failed the first time — a bad/inaccessible file isn't retried
// every cycle).
func extractMetadataIfNew(path string) *AppMetadata {
	if _, alreadySeen := metaExtracted.LoadOrStore(path, struct{}{}); alreadySeen {
		return nil
	}

	meta := &AppMetadata{}

	if fi, err := os.Stat(path); err == nil {
		meta.Size = fi.Size()
		meta.FileModifiedAt = fi.ModTime()
	}
	if ct, err := getFileCreationTime(path); err == nil {
		meta.FileCreatedAt = ct
	}

	if info, err := readVersionInfo(path); err == nil {
		meta.ProductName = info["ProductName"]
		meta.Company = info["CompanyName"]
		meta.Description = info["FileDescription"]
		meta.ProductVersion = info["ProductVersion"]
	}

	return meta
}

// getFileCreationTime reads the Windows-specific creation timestamp, which
// os.FileInfo.ModTime() does not expose.
func getFileCreationTime(path string) (time.Time, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return time.Time{}, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return time.Time{}, err
	}
	defer windows.CloseHandle(h)

	var created, access, write windows.Filetime
	if err := windows.GetFileTime(h, &created, &access, &write); err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, created.Nanoseconds()), nil
}

// readVersionInfo extracts the standard VERSIONINFO string fields (Product
// Name, Company Name, File Description, Product Version) embedded in a
// Windows PE file's resources, via the same GetFileVersionInfo/VerQueryValue
// APIs Explorer's "Details" tab uses. Returns an error if the file has no
// version resource (common for scripts/portable tools — not treated as fatal
// by the caller).
func readVersionInfo(path string) (map[string]string, error) {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil || size == 0 {
		return nil, err
	}

	buf := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&buf[0])); err != nil {
		return nil, err
	}

	// VERSIONINFO string tables are keyed by a language/codepage pair. Query
	// the translation table first, then fall back to the common
	// "040904b0" (US English, Unicode) block most installers use.
	langCodePage := "040904b0"
	var translatePtr unsafe.Pointer
	var translateLen uint32
	if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), `\VarFileInfo\Translation`,
		unsafe.Pointer(&translatePtr), &translateLen); err == nil && translateLen >= 4 {
		langs := unsafe.Slice((*uint16)(translatePtr), translateLen/2)
		langCodePage = uint16ToHex(langs[0]) + uint16ToHex(langs[1])
	}

	fields := map[string]string{}
	for _, key := range []string{"ProductName", "CompanyName", "FileDescription", "ProductVersion"} {
		subBlock := `\StringFileInfo\` + langCodePage + `\` + key
		var valuePtr unsafe.Pointer
		var valueLen uint32
		if err := windows.VerQueryValue(unsafe.Pointer(&buf[0]), subBlock,
			unsafe.Pointer(&valuePtr), &valueLen); err != nil || valueLen == 0 {
			continue
		}
		u16 := unsafe.Slice((*uint16)(valuePtr), valueLen)
		fields[key] = windows.UTF16ToString(u16)
	}
	return fields, nil
}

func uint16ToHex(v uint16) string {
	const hexDigits = "0123456789abcdef"
	b := [4]byte{
		hexDigits[(v>>12)&0xf],
		hexDigits[(v>>8)&0xf],
		hexDigits[(v>>4)&0xf],
		hexDigits[v&0xf],
	}
	return string(b[:])
}
