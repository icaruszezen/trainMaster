package dataset

import (
	"strings"
	"unicode/utf8"
)

// Limits for user-provided names and texts. The frontend
// (frontend/src/features/datasets/naming.ts) applies the same rules.
const (
	maxNameRunes        = 64
	maxNameBytes        = 200 // keeps folder names well below the usual 255-byte file name limit
	maxDescriptionRunes = 500
	maxFileNameRunes    = 255
)

// forbiddenNameChars cannot appear in a dataset name because the name is
// also the folder name, and these are invalid in Windows file names (the
// data directory may be copied to or shared with Windows machines).
const forbiddenNameChars = `\/:*?"<>|`

// windowsReservedNames are device names Windows refuses as file names, also
// with an extension ("con.txt").
var windowsReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// NormalizeName trims a dataset name and validates it. The result is also
// usable as a folder name on Linux, macOS and Windows.
func NormalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	fail := func(msg string) (string, error) { return "", &Error{Kind: ErrInvalid, Message: msg} }
	switch {
	case name == "":
		return fail("名称不能为空")
	case !utf8.ValidString(name):
		return fail("名称包含无效字符")
	case utf8.RuneCountInString(name) > maxNameRunes:
		return fail("名称不能超过 64 个字符")
	case len(name) > maxNameBytes:
		return fail("名称过长，请缩短后重试")
	case strings.ContainsAny(name, forbiddenNameChars):
		return fail(`名称不能包含 \ / : * ? " < > | 等字符`)
	case hasControlChar(name):
		return fail("名称不能包含换行、制表符等控制字符")
	case strings.HasPrefix(name, "."):
		return fail("名称不能以“.”开头")
	case strings.HasSuffix(name, ".") || strings.HasSuffix(name, " "):
		return fail("名称不能以“.”或空格结尾")
	case isWindowsReservedName(name):
		return fail("该名称是系统保留名称")
	}
	return name, nil
}

// NormalizeDescription trims a description and checks its length.
func NormalizeDescription(raw string) (string, error) {
	d := strings.TrimSpace(raw)
	if !utf8.ValidString(d) {
		return "", &Error{Kind: ErrInvalid, Message: "描述包含无效字符"}
	}
	if utf8.RuneCountInString(d) > maxDescriptionRunes {
		return "", &Error{Kind: ErrInvalid, Message: "描述不能超过 500 个字符"}
	}
	return d, nil
}

func hasControlChar(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func isWindowsReservedName(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	// Windows also ignores trailing spaces before the extension ("CON .txt").
	return windowsReservedNames[strings.ToUpper(strings.TrimRight(stem, " "))]
}

// validDirName reports whether name is a single, visible path segment that
// can be joined onto the datasets directory without escaping it. Hidden
// names (including "." and "..") are rejected.
func validDirName(name string) bool {
	return name != "" && !strings.HasPrefix(name, ".") && !strings.ContainsAny(name, "/\\\x00") && utf8.ValidString(name)
}
