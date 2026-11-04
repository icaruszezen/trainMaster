package review

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Limits for user input. The frontend (frontend/src/features/reviews/)
// applies the same rules.
const (
	maxNameRunes        = 64  // as for projects
	maxDescriptionRunes = 500 // as for projects
	maxErrorRunes       = 500
	maxDetailBytes      = 8 << 10
	maxLabelRunes       = 255 // as for dataset annotation labels
)

// normalizeName trims a review name and validates it with the rules of
// project names.
func normalizeName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "":
		return "", &Error{Kind: ErrInvalid, Message: "名称不能为空"}
	case !utf8.ValidString(name):
		return "", &Error{Kind: ErrInvalid, Message: "名称包含无效字符"}
	case utf8.RuneCountInString(name) > maxNameRunes:
		return "", &Error{Kind: ErrInvalid, Message: "名称不能超过 64 个字符"}
	case strings.ContainsFunc(name, isControlChar):
		return "", &Error{Kind: ErrInvalid, Message: "名称不能包含换行、制表符等控制字符"}
	}
	return name, nil
}

// normalizeDescription trims a description and validates it with the rules
// of project descriptions: line breaks and tabs are allowed, other control
// characters are not.
func normalizeDescription(raw string) (string, error) {
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

// defaultName is the name of a review created without one:
// "<project> v<version> 审图", the project name shortened to fit.
func defaultName(projectName string, version int) string {
	suffix := " v" + strconv.Itoa(version) + " 审图"
	room := maxNameRunes - utf8.RuneCountInString(suffix)
	return strings.TrimSpace(truncateRunes(cleanLabel(projectName, maxNameRunes), room)) + suffix
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

// cleanText makes text from a node storable and printable: valid UTF-8
// without NUL or other control characters except line breaks and tabs,
// and at most n runes.
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
// breaks and tabs become spaces, other control characters (also the C1
// ones) are dropped, and the result is trimmed.
func cleanLabel(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r >= 0x80 && r < 0xa0:
			return -1
		}
		return r
	}, cleanText(s, n))
	return strings.TrimSpace(s)
}
