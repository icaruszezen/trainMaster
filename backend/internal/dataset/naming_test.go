package dataset

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	ok := map[string]string{
		"划痕-A":                  "划痕-A",
		"  batch 1  ":           "batch 1",
		"line2.v3":              "line2.v3",
		"COM10":                 "COM10",
		"console":               "console",
		strings.Repeat("字", 64): strings.Repeat("字", 64),
	}
	for in, want := range ok {
		got, err := NormalizeName(in)
		if err != nil || got != want {
			t.Errorf("NormalizeName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	bad := map[string]string{
		"":                        "名称不能为空",
		"   ":                     "名称不能为空",
		strings.Repeat("字", 65):   "名称不能超过 64 个字符",
		strings.Repeat("😀", 51):   "名称过长，请缩短后重试", // 51 runes but 204 bytes
		"a/b":                     `名称不能包含 \ / : * ? " < > | 等字符`,
		`a\b`:                     `名称不能包含 \ / : * ? " < > | 等字符`,
		"a:b":                     `名称不能包含 \ / : * ? " < > | 等字符`,
		"a|b":                     `名称不能包含 \ / : * ? " < > | 等字符`,
		"a\tb":                    "名称不能包含换行、制表符等控制字符",
		"a\x7fb":                  "名称不能包含换行、制表符等控制字符",
		".hidden":                 "名称不能以“.”开头",
		"..":                      "名称不能以“.”开头",
		"name.":                   "名称不能以“.”或空格结尾",
		"CON":                     "该名称是系统保留名称",
		"nul":                     "该名称是系统保留名称",
		"com1.txt":                "该名称是系统保留名称",
		"LPT9 .zip":               "该名称是系统保留名称",
		"aux.tar.gz":              "该名称是系统保留名称",
		string([]byte{0xff, 'a'}): "名称包含无效字符",
	}
	for in, msg := range bad {
		_, err := NormalizeName(in)
		var ue *Error
		if !errors.As(err, &ue) || !errors.Is(err, ErrInvalid) || ue.Message != msg {
			t.Errorf("NormalizeName(%q) error = %v; want %q", in, err, msg)
		}
	}
}

func TestNormalizeDescription(t *testing.T) {
	if d, err := NormalizeDescription("  产线 2 的划痕样本 \n"); err != nil || d != "产线 2 的划痕样本" {
		t.Errorf("got %q, %v", d, err)
	}
	if _, err := NormalizeDescription(strings.Repeat("字", 500)); err != nil {
		t.Errorf("500 characters should be allowed: %v", err)
	}
	if _, err := NormalizeDescription(strings.Repeat("字", 501)); !errors.Is(err, ErrInvalid) {
		t.Errorf("501 characters: %v", err)
	}
}

func TestValidDirName(t *testing.T) {
	for _, name := range []string{"ds1", "划痕 A", "a.b", "con"} {
		if !validDirName(name) {
			t.Errorf("validDirName(%q) = false", name)
		}
	}
	for _, name := range []string{"", ".", "..", ".git", "a/b", `a\b`, "a\x00b", string([]byte{0xff})} {
		if validDirName(name) {
			t.Errorf("validDirName(%q) = true", name)
		}
	}
}
