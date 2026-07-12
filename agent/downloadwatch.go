package main

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// FILE_NOTIFY_INFORMATION action codes (winnt.h) — not exported by
// golang.org/x/sys/windows, so defined here.
const (
	fileActionAdded          = 1
	fileActionRemoved        = 2
	fileActionModified       = 3
	fileActionRenamedOldName = 4
	fileActionRenamedNewName = 5
)

// watchedFolder describes one of the three folders Module 2 monitors, with
// the execution_location label used both in the event's metadata and by
// server-side policy matching (server/policy.go's classifyExecutionLocation
// uses the same three names for the process-execution side of things).
type watchedFolder struct {
	path     string
	location string
}

// startDownloadWatch watches Downloads/Desktop/Documents for file
// create/rename/delete via ReadDirectoryChangesW — event-driven, no polling.
// One goroutine per folder; a missing folder (unusual profile layout) is
// logged and skipped rather than failing the others.
func startDownloadWatch(ctx context.Context, agentID string) {
	home, err := os.UserHomeDir()
	if err != nil {
		logMsg("ERROR", "downloadwatch: cannot resolve home dir: %v", err)
		return
	}
	folders := []watchedFolder{
		{filepath.Join(home, "Downloads"), "downloads"},
		{filepath.Join(home, "Desktop"), "desktop"},
		{filepath.Join(home, "Documents"), "documents"},
	}
	for _, f := range folders {
		go watchFolder(ctx, agentID, f)
	}
}

func watchFolder(ctx context.Context, agentID string, folder watchedFolder) {
	pathPtr, err := windows.UTF16PtrFromString(folder.path)
	if err != nil {
		logMsg("ERROR", "downloadwatch: invalid path %s: %v", folder.path, err)
		return
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		logMsg("WARN", "downloadwatch: cannot open %s, skipping: %v", folder.path, err)
		return
	}
	defer windows.CloseHandle(handle)

	go func() {
		<-ctx.Done()
		windows.CloseHandle(handle) // unblocks the pending synchronous ReadDirectoryChanges call below
	}()

	const mask = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_SIZE
	buf := make([]byte, 64*1024)

	for {
		var bytesReturned uint32
		if err := windows.ReadDirectoryChanges(handle, &buf[0], uint32(len(buf)), false, mask, &bytesReturned, nil, 0); err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			logMsg("ERROR", "downloadwatch: ReadDirectoryChanges(%s) failed: %v", folder.path, err)
			return
		}
		if bytesReturned == 0 {
			continue
		}
		handleNotifyBuffer(agentID, folder, buf[:bytesReturned])
	}
}

func handleNotifyBuffer(agentID string, folder watchedFolder, buf []byte) {
	offset := 0
	for {
		if offset+12 > len(buf) {
			return
		}
		rec := buf[offset:]
		nextEntryOffset := binary.LittleEndian.Uint32(rec[0:4])
		action := binary.LittleEndian.Uint32(rec[4:8])
		nameLen := binary.LittleEndian.Uint32(rec[8:12])

		if int(12+nameLen) <= len(rec) {
			name := decodeUTF16Name(rec[12 : 12+nameLen])
			handleFileEvent(agentID, folder, action, name)
		}

		if nextEntryOffset == 0 {
			return
		}
		offset += int(nextEntryOffset)
	}
}

func decodeUTF16Name(b []byte) string {
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(b[i*2 : i*2+2])
	}
	return windows.UTF16ToString(u16)
}

func handleFileEvent(agentID string, folder watchedFolder, action uint32, name string) {
	switch action {
	case fileActionAdded, fileActionRenamedNewName:
		fullPath := filepath.Join(folder.path, name)
		ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
		var size int64
		if fi, err := os.Stat(fullPath); err == nil {
			size = fi.Size()
		}
		sendEvent(agentID, "download_created", map[string]interface{}{
			"path":      fullPath,
			"extension": ext,
			"size":      size,
			"location":  folder.location,
		})

	case fileActionRemoved:
		fullPath := filepath.Join(folder.path, name)
		sendEvent(agentID, "download_deleted", map[string]interface{}{
			"path":     fullPath,
			"location": folder.location,
		})

	case fileActionModified, fileActionRenamedOldName:
		// Modified is too noisy to report per-write for a library PC
		// (every download's in-progress write triggers it); RenamedOldName
		// is always immediately followed by RenamedNewName above, which is
		// the signal that matters.
	}
}
