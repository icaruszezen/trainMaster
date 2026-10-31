package project

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	for raw, want := range map[string]string{
		"  产线 A 外观检测 ":          "产线 A 外观检测",
		"a/b:c*?":               "a/b:c*?", // not a folder name: any printable character
		".hidden.":              ".hidden.",
		"CON":                   "CON",
		strings.Repeat("项", 64): strings.Repeat("项", 64),
	} {
		if got, err := NormalizeName(raw); err != nil || got != want {
			t.Errorf("NormalizeName(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for raw, msg := range map[string]string{
		"":                      "名称不能为空",
		" \t ":                  "名称不能为空",
		strings.Repeat("项", 65): "名称不能超过 64 个字符",
		"a\nb":                  "控制字符",
		"a\x7fb":                "控制字符",
		"\xff":                  "无效字符",
	} {
		_, err := NormalizeName(raw)
		var pe *Error
		if !errors.As(err, &pe) || !errors.Is(err, ErrInvalid) || !strings.Contains(pe.Message, msg) {
			t.Errorf("NormalizeName(%q) error = %v, want %q", raw, err, msg)
		}
	}
}

func TestNormalizeDescription(t *testing.T) {
	if got, err := NormalizeDescription("  多行\n描述  "); err != nil || got != "多行\n描述" {
		t.Errorf("got %q, %v", got, err)
	}
	if _, err := NormalizeDescription(strings.Repeat("字", 501)); !errors.Is(err, ErrInvalid) {
		t.Errorf("501 characters: %v", err)
	}
	if _, err := NormalizeDescription(strings.Repeat("字", 500)); err != nil {
		t.Errorf("500 characters: %v", err)
	}
	if got, err := NormalizeDescription("列1\t列2\r\n下一行"); err != nil || got != "列1\t列2\r\n下一行" {
		t.Errorf("tabs and line breaks: %q, %v", got, err)
	}
	for _, d := range []string{"a\x00b", "a\x1bb", "a\x7fb", "a\xffb"} {
		if _, err := NormalizeDescription(d); !errors.Is(err, ErrInvalid) {
			t.Errorf("NormalizeDescription(%q) = %v, want ErrInvalid", d, err)
		}
	}
}

func TestNormalizeIDs(t *testing.T) {
	got, err := normalizeIDs([]int64{5, 1, 5, 3, 1}, "数据集")
	if err != nil || !slices.Equal(got, []int64{1, 3, 5}) {
		t.Errorf("normalizeIDs = %v, %v", got, err)
	}
	if got, err := normalizeIDs(nil, "数据集"); err != nil || len(got) != 0 {
		t.Errorf("nil = %v, %v", got, err)
	}
	input := []int64{3, 1}
	if _, err := normalizeIDs(input, "x"); err != nil || !slices.Equal(input, []int64{3, 1}) {
		t.Errorf("the input must not be modified: %v", input)
	}
	for _, ids := range [][]int64{{1, 0}, {-2}} {
		if _, err := normalizeIDs(ids, "项目"); !errors.Is(err, ErrInvalid) {
			t.Errorf("normalizeIDs(%v) = %v, want ErrInvalid", ids, err)
		}
	}
	if _, err := normalizeIDs(make([]int64, maxIDs+1), "项目"); !errors.Is(err, ErrInvalid) {
		t.Errorf("too many IDs: %v", err)
	}
}

func TestInterleave(t *testing.T) {
	groups := [][]string{{"a1", "a2", "a3", "a4"}, {"b1"}, {"c1", "c2"}}
	if got := interleave(groups, 4); !slices.Equal(got, []string{"a1", "b1", "c1", "a2"}) {
		t.Errorf("interleave = %v", got)
	}
	if got := interleave(groups[1:], 4); !slices.Equal(got, []string{"b1", "c1", "c2"}) {
		t.Errorf("fewer than max = %v", got)
	}
	if got := interleave[string](nil, 4); len(got) != 0 {
		t.Errorf("no groups = %v", got)
	}
}
