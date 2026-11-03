package model

import (
	"strings"
	"unicode/utf8"
)

// Limits for user input. The frontend (frontend/src/features/models/)
// applies the same rules.
const (
	maxDescriptionRunes = 500 // as for projects
	maxFileNameRunes    = 255
	// MaxModelBytes is the largest weights file accepted (8 GiB).
	MaxModelBytes = 8 << 30
)

// NormalizeDescription trims a model description and validates it, with
// the rules of project descriptions: line breaks and tabs are allowed,
// other control characters (PostgreSQL text cannot hold NUL at all) are
// not.
func NormalizeDescription(raw string) (string, error) {
	d := strings.TrimSpace(raw)
	invalid := strings.ContainsFunc(d, func(r rune) bool {
		return r != '\n' && r != '\r' && r != '\t' && isControlChar(r)
	})
	if invalid || !utf8.ValidString(d) {
		return "", &Error{Kind: ErrInvalid, Message: "说明包含无效字符"}
	}
	if utf8.RuneCountInString(d) > maxDescriptionRunes {
		return "", &Error{Kind: ErrInvalid, Message: "说明不能超过 500 个字符"}
	}
	return d, nil
}

// normalizeFileName validates the original name of an uploaded weights
// file. It is only shown (downloads are named after the project), so the
// rules are about display: a .pt name without control characters.
func normalizeFileName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	fail := func(msg string) (string, error) { return "", &Error{Kind: ErrInvalid, Message: msg} }
	switch {
	case name == "":
		return fail("请提供模型文件名")
	case !utf8.ValidString(name) || strings.ContainsFunc(name, isControlChar):
		return fail("文件名包含无效字符")
	case utf8.RuneCountInString(name) > maxFileNameRunes:
		return fail("文件名不能超过 255 个字符")
	case !strings.HasSuffix(strings.ToLower(name), ".pt"):
		return fail("请选择 .pt 格式的模型文件（PyTorch 权重，如 best.pt）")
	}
	return name, nil
}

func isControlChar(r rune) bool { return r < 0x20 || r == 0x7f }

// truncateRunes cuts s to at most n runes.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// truncateBytes cuts s to at most n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// cleanText makes text from a file or a node storable and printable:
// valid UTF-8 without NUL (which PostgreSQL text and jsonb cannot hold) or
// other control characters except line breaks and tabs, and at most n
// runes.
func cleanText(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if r != '\n' && r != '\r' && r != '\t' && isControlChar(r) {
			return -1
		}
		return r
	}, s)
	return truncateRunes(s, n)
}

// cleanLabel is cleanText for single-line values (names, versions): line
// breaks and tabs become spaces, and the result is trimmed.
func cleanLabel(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, cleanText(s, n))
	return strings.TrimSpace(s)
}
