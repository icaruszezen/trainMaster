package project

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// Limits for user input. The frontend (frontend/src/features/projects/
// naming.ts) applies the same rules.
const (
	maxNameRunes        = 64
	maxDescriptionRunes = 500
	// maxIDs bounds the dataset or project IDs of one request.
	maxIDs = 1000
)

// NormalizeName trims a project name and validates it. Unlike dataset
// names, project names are never used as folder names, so only control
// characters are rejected.
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
	case hasControlChar(name):
		return fail("名称不能包含换行、制表符等控制字符")
	}
	return name, nil
}

// NormalizeDescription trims a description and checks its length. Line
// breaks and tabs are allowed; other control characters (PostgreSQL text
// cannot hold NUL at all) are not.
func NormalizeDescription(raw string) (string, error) {
	d := strings.TrimSpace(raw)
	invalid := strings.ContainsFunc(d, func(r rune) bool {
		return r != '\n' && r != '\r' && r != '\t' && isControlChar(r)
	})
	if invalid || !utf8.ValidString(d) {
		return "", &Error{Kind: ErrInvalid, Message: "描述包含无效字符"}
	}
	if utf8.RuneCountInString(d) > maxDescriptionRunes {
		return "", &Error{Kind: ErrInvalid, Message: "描述不能超过 500 个字符"}
	}
	return d, nil
}

func hasControlChar(s string) bool {
	return strings.ContainsFunc(s, isControlChar)
}

func isControlChar(r rune) bool { return r < 0x20 || r == 0x7f }

// normalizeIDs validates a list of IDs from a request and returns it
// sorted without duplicates; what names the kind of ID in messages.
func normalizeIDs(ids []int64, what string) ([]int64, error) {
	if len(ids) > maxIDs {
		return nil, newError(ErrInvalid, "一次最多选择 %d 个%s", maxIDs, what)
	}
	out := slices.Clone(ids)
	for _, id := range out {
		if id <= 0 {
			return nil, newError(ErrInvalid, "%s ID 无效：%d", what, id)
		}
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}
