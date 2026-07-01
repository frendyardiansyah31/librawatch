package main

import (
	"strings"
	"testing"
)

// escapePSArg mirrors the agent's quote-escaping logic for unit testing.
func escapePSArg(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// ── validateDeployRequest ─────────────────────────────────────────────────

// Positive: valid exec payload passes.
func TestValidateDeploy_Exec_Valid(t *testing.T) {
	// Arrange + Act
	err := validateDeployRequest("exec", "Get-Process | Select Name, CPU", "")

	// Assert
	if err != nil {
		t.Fatalf("expected no error for valid exec payload, got: %v", err)
	}
}

// Negative: exec payload exceeding 8192 chars is rejected.
func TestValidateDeploy_Exec_TooLong(t *testing.T) {
	// Arrange
	huge := make([]byte, 8193)
	for i := range huge {
		huge[i] = 'A'
	}

	// Act
	err := validateDeployRequest("exec", string(huge), "")

	// Assert
	if err == nil {
		t.Fatal("expected error for oversized exec payload, got nil")
	}
}

// Negative: exec payload with null byte is rejected.
func TestValidateDeploy_Exec_NullByte(t *testing.T) {
	// Arrange + Act
	err := validateDeployRequest("exec", "Get-Process\x00evil", "")

	// Assert
	if err == nil {
		t.Fatal("expected error for null byte in exec payload, got nil")
	}
}

// Positive: valid winget install command passes.
func TestValidateDeploy_Winget_ValidInstall(t *testing.T) {
	// Arrange + Act
	err := validateDeployRequest("winget",
		"winget install --id Notepad++.Notepad++ --silent --accept-source-agreements --accept-package-agreements", "")

	// Assert
	if err != nil {
		t.Fatalf("expected no error for valid winget install, got: %v", err)
	}
}

// Positive: valid winget uninstall command passes.
func TestValidateDeploy_Winget_ValidUninstall(t *testing.T) {
	// Arrange + Act
	err := validateDeployRequest("winget",
		"winget uninstall --id Microsoft.VisualStudioCode --silent", "")

	// Assert
	if err != nil {
		t.Fatalf("expected no error for valid winget uninstall, got: %v", err)
	}
}

// Negative: winget with semicolon injection in package ID is rejected.
func TestValidateDeploy_Winget_InjectionSemicolon(t *testing.T) {
	// Arrange + Act — attacker appends "; Remove-Item C:\Windows -Recurse"
	err := validateDeployRequest("winget",
		"winget install --id evil; Remove-Item C:\\Windows -Recurse --silent", "")

	// Assert
	if err == nil {
		t.Fatal("expected error for winget injection attempt, got nil")
	}
}

// Negative: winget with backtick in package ID is rejected.
func TestValidateDeploy_Winget_InjectionBacktick(t *testing.T) {
	// Arrange + Act
	err := validateDeployRequest("winget",
		"winget install --id foo`bar --silent", "")

	// Assert
	if err == nil {
		t.Fatal("expected error for winget package ID with backtick, got nil")
	}
}

// Negative: winget with wrong verb is rejected.
func TestValidateDeploy_Winget_WrongVerb(t *testing.T) {
	// Arrange + Act
	err := validateDeployRequest("winget",
		"winget remove --id Notepad++.Notepad++ --silent", "")

	// Assert
	if err == nil {
		t.Fatal("expected error for winget with unsupported verb, got nil")
	}
}

// Positive: file_deploy with safe args passes.
func TestValidateDeploy_FileDeploy_SafeArgs(t *testing.T) {
	// Arrange + Act
	err := validateDeployRequest("file_deploy", "installer.exe", "/S /silent")

	// Assert
	if err != nil {
		t.Fatalf("expected no error for safe file_deploy args, got: %v", err)
	}
}

// Negative: file_deploy args exceeding 512 chars is rejected.
func TestValidateDeploy_FileDeploy_ArgsTooLong(t *testing.T) {
	// Arrange
	longArgs := make([]byte, 513)
	for i := range longArgs {
		longArgs[i] = 'x'
	}

	// Act
	err := validateDeployRequest("file_deploy", "installer.exe", string(longArgs))

	// Assert
	if err == nil {
		t.Fatal("expected error for oversized args, got nil")
	}
}

// Positive: valid deepfreeze actions pass.
func TestValidateDeploy_DeepFreeze_ValidActions(t *testing.T) {
	for _, action := range []string{"thaw", "freeze", "query_df"} {
		err := validateDeployRequest("deepfreeze", action, "")
		if err != nil {
			t.Fatalf("expected no error for deepfreeze action %q, got: %v", action, err)
		}
	}
}

// Negative: unknown deepfreeze action is rejected.
func TestValidateDeploy_DeepFreeze_InvalidAction(t *testing.T) {
	// Arrange + Act
	err := validateDeployRequest("deepfreeze", "delete_all", "")

	// Assert
	if err == nil {
		t.Fatal("expected error for invalid deepfreeze action, got nil")
	}
}

// Negative: unknown deploy type is rejected.
func TestValidateDeploy_UnknownType_Rejected(t *testing.T) {
	// Arrange + Act
	err := validateDeployRequest("rm_rf", "payload", "")

	// Assert
	if err == nil {
		t.Fatal("expected error for unknown deploy type, got nil")
	}
}

// ── PS quote escaping (agent side) ────────────────────────────────────────

// Positive: args without quotes pass through unchanged.
func TestEscapePS_NoQuotes_Unchanged(t *testing.T) {
	// Arrange
	input := "/silent /norestart"

	// Act
	result := escapePSArg(input)

	// Assert
	if result != input {
		t.Fatalf("expected %q unchanged, got %q", input, result)
	}
}

// Positive: single quote in args is doubled (PS escape convention).
func TestEscapePS_SingleQuote_Doubled(t *testing.T) {
	// Arrange
	input := "it's here"

	// Act
	result := escapePSArg(input)

	// Assert
	if result != "it''s here" {
		t.Fatalf("expected single quote doubled, got %q", result)
	}
}

// Negative: unescaped single quote would allow PS injection (demonstrates why fix is needed).
func TestEscapePS_InjectionPrevented(t *testing.T) {
	// Arrange — attacker tries to break out of PS string via single quote
	malicious := "' ; Start-Process evil.exe ; '"

	// Act — escaped version cannot break out
	escaped := escapePSArg(malicious)

	// Assert — every ' in the escaped string must be paired as '' (PS escape convention).
	// Walk byte-by-byte; skip over valid '' pairs; fail on any lone '.
	i := 0
	for i < len(escaped) {
		if escaped[i] == '\'' {
			if i+1 < len(escaped) && escaped[i+1] == '\'' {
				i += 2 // valid '' pair, skip both
				continue
			}
			t.Fatalf("found lone single quote at position %d in escaped string: %q", i, escaped)
		}
		i++
	}
}
