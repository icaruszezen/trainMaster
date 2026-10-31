package dataset

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
	"golang.org/x/text/width"
)

// Image order. A dataset's images are listed, shown in the viewer (previous
// / next), picked as covers and downloaded in one fixed order: the default
// "Name" order of Windows Explorer on a Chinese (zh-CN) Windows. Explorer
// compares names with StrCmpLogicalW, which since Windows 7 behaves like
// CompareStringEx with NORM_IGNORECASE | SORT_DIGITSASNUMBERS:
//
//   - The dataset is walked like a folder tree: at each level the folders
//     come first (each with everything below it), then the files.
//   - A name is compared as a whole, extension included, ignoring case and
//     width (full-width "Ａ" and "１" are "a" and "1"). Each run of digits
//     is compared as a number: img2 < img10, and of two equal numbers the
//     one with more leading zeros comes first (img01 < img1).
//   - Spaces and punctuation come first, in the order of Windows' sort
//     table (space ! # $ % & ( ) , . ; @ [ ] ^ _ ` { } ~ + =, so "a.jpg" <
//     "a_1.jpg"), then numbers, then the other number characters by value
//     (½ < ① < ⑩ < Ⅰ < Ⅳ), then letters (alphabetically, accents ignored),
//     then Chinese characters in pinyin order.
//   - Hyphens and apostrophes are skipped at first, as in Windows' "word
//     sort" ("a-b" sorts next to "ab"); of two names equal without them,
//     the one without comes first.
//   - Names that are still equal (A.jpg and a.jpg, which Windows cannot
//     hold side by side) are ordered by their characters' code points, so
//     the order never depends on when the files were indexed.
//
// ImageSortKey encodes this order in a byte string that dataset_images
// stores in sort_key: comparing two keys byte by byte gives the order of
// the two paths, so the database sorts and pages through images with an
// index (ORDER BY ImageOrder).

// imageOrderRevision numbers the rule ImageSortKey implements. Increase it
// whenever the rule changes (TestImageSortKeyStable fails then): the keys
// stored for another version are recomputed when the server starts.
const imageOrderRevision = 1

// imageOrderVersion identifies the keys stored in sort_key: the rule and
// the Unicode tables it reads (golang.org/x/text's collation, width and
// normalization tables, and Go's unicode package), so that keys are also
// recomputed after an upgrade of Go or golang.org/x/text changes them.
var imageOrderVersion = fmt.Sprintf("%d/cldr-%s/collate-%s/width-%s/norm-%s/go-unicode-%s", imageOrderRevision,
	collate.CLDRVersion, collate.UnicodeVersion, width.UnicodeVersion, norm.Version, unicode.Version)

// imageOrderSetting is the app_settings key recording the imageOrderVersion
// of the stored keys.
const imageOrderSetting = "dataset.image_order"

// maxSortKeyLen bounds a key so it fits in a B-tree index entry (about
// 2.7 KB); paths too long for it are ordered by their beginning, ties by
// ID. That takes paths of several hundred characters.
const maxSortKeyLen = 2048

// Layout of a key. Each segment of the path (a folder, then the file name)
// starts with keyFolder or keyFile and continues with the key of its name:
//
//	tokens keyEnd specials keyEnd name keyEnd
//
// tokens are the characters compared first, in tokens of five kinds whose
// first bytes order them: a symbol (its group and rank in Windows' table),
// a number (count of significant digits, digits, then leading zeros), a
// numeral (its rank in Windows' table: ½, ①, Ⅳ), another number character
// (collation weights) and a run of letters (collation weights). specials
// are the hyphens and apostrophes set aside, each with its place in the
// name. name is the name itself, the final tie-break. Every part ends with
// a byte smaller than any that could continue it, so a prefix comes first.
const (
	keyEnd    = 0x00
	keyFolder = 0x01
	keyFile   = 0x02

	tokSymbol      = 0x01
	tokNumber      = 0x02
	tokNumeral     = 0x03
	tokOtherNumber = 0x04
	tokText        = 0x05

	special = 0x01 // starts an entry of specials

	rankOther = 0xFF // a symbol missing from windowsSymbols, after those of its group
)

// windowsSymbols lists, by group, the spaces, punctuation and symbols of
// Windows' default sort table (also used for zh-CN) that file names
// commonly contain, those of GB 2312 included, in the table's order.
// Groups sort in index order, all before digits. Full-width forms are
// folded to these before the lookup.
var windowsSymbols = [...]string{
	7: " \u00a0\t\n\v\f\r\u2000\u2001\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u200b\u2028\u2029" +
		"!\"#$%&()*,、.。/:;?@[\\]^_`{|}~¡¨¯´¿ˇ˙‖‘’‚‛“”„‟′‹›〈〉¢£》¥「」『』【】〔€〕〖〗〘〙〚〛〝〞〟〃",
	8:  "+⊗⁄⊙<=>±«»×÷∈∏∑√∝∠∥∧∨∩∪∫∮∴∵∶∷∽≈≌≡≤≥⊥",
	9:  "⌒■▲◆○↑→↓←↔",
	10: "§©®°¶·・‥…〒〓☆★♀♂",
	11: "†‡•‰※",
}

// symbolTwins maps characters Windows' table weighs the same as one of
// windowsSymbols to that character.
var symbolTwins = map[rune]rune{
	'\u1680': ' ', '\u180e': ' ', '\u202f': ' ', '\u205f': ' ', // other spaces
	'\u2002': '\u2000', '\u2003': '\u2001', // en and em space, as the en and em quad
	'ˉ': '*', '″': '′', '《': '¢', '¤': '》', '⊕': '+', '≮': '<', '≠': '=', '≯': '>',
	'□': '■', '△': '▲', '◇': '◆', '◎': '○', '●': '○', '⇒': '→',
}

// windowsNumerals lists the number characters other than decimal digits
// that file names commonly contain (superscripts, fractions, circled and
// parenthesized numbers, Roman numerals, ...), in the order of Windows'
// sort table, where they come after all numbers and before the letters.
// Each string holds characters the table weighs the same.
var windowsNumerals = [...]string{
	"⁰₀↉⓪⓿㍘", "⅒", "⅑", "⅛", "⅐", "⅙", "⅕", "¼", "⅓", "⅜", "⅖", "½", "⅗", "⅝", "⅔", "¾", "⅘", "⅚", "⅞",
	"¹₁①⑴⒈⓵⚀❶➀➊〡㋀㍙㏠", "²₂②⑵⒉⓶⚁❷➁➋〢㋁㍚㏡", "³₃③⑶⒊⓷⚂❸➂➌〣㋂㍛㏢", "⁴₄④⑷⒋⓸⚃❹➃➍〤㋃㍜㏣",
	"⁵₅⑤⑸⒌⓹⚄❺➄➎〥㋄㍝㏤", "⁶₆ↅ⑥⑹⒍⓺⚅❻➅➏〦㋅㍞㏥", "⁷₇⑦⑺⒎⓻❼➆➐〧㋆㍟㏦", "⁸₈⑧⑻⒏⓼❽➇➑〨㋇㍠㏧",
	"⁹₉⑨⑼⒐⓽❾➈➒〩㋈㍡㏨", "⑩⑽⒑⓾❿➉➓〸㉈㋉㋊㍢㏩", "⑪⑾⒒⓫㋋㍣㏪", "⑫⑿⒓⓬㍤㏫", "⑬⒀⒔⓭㍥㏬", "⑭⒁⒕⓮㍦㏭",
	"⑮⒂⒖⓯㍧㏮", "⑯⒃⒗⓰㍨㏯", "⑰⒄⒘⓱㍩㏰", "⑱⒅⒙⓲㍪㏱", "⑲⒆⒚⓳㍫㏲", "⑳⒇⒛⓴〹㉉㍬㏳", "㉑㍭㏴", "㉒㍮㏵",
	"㉓㍯㏶", "㉔㍰㏷", "㉕㏸", "㉖㏹", "㉗㏺", "㉘㏻", "㉙㏼", "〺㉊㉚㏽", "㉛㏾", "㉜", "㉝", "㉞", "㉟", "㊱",
	"㊲", "㊳", "㊴", "㉋㊵", "㊶", "㊷", "㊸", "㊹", "㊺", "㊻", "㊼", "㊽", "㊾", "ↆ㉌㊿", "㉍", "㉎", "㉏", "⅟",
	"Ⅰⅰ", "Ⅱⅱ", "Ⅲⅲ", "Ⅳⅳ", "Ⅴⅴ", "Ⅵⅵ", "Ⅶⅶ", "Ⅷⅷ", "Ⅸⅸ", "Ⅹⅹ", "Ⅺⅺ", "Ⅻⅻ", "Ⅼⅼ", "ⅭⅽↃↄ", "Ⅾⅾ",
	"Ⅿⅿↀ", "ↁ", "ↂ", "ↇ", "ↈ", "∞⧜⧝⧞",
}

// hanNumerals are the numbers one to ten in parentheses and in circles.
// Windows sorts them in this order after the Chinese characters; here they
// follow the other numerals.
const hanNumerals = "㈠㈡㈢㈣㈤㈥㈦㈧㈨㈩㊀㊁㊂㊃㊄㊅㊆㊇㊈㊉"

// numeralRanks maps the characters of windowsNumerals and hanNumerals to
// their rank.
var numeralRanks = func() map[rune]byte {
	m := map[rune]byte{}
	rank := byte(1)
	for _, chars := range windowsNumerals {
		for _, r := range chars {
			m[r] = rank
		}
		rank++
	}
	for _, r := range hanNumerals {
		m[r] = rank
		rank++
	}
	return m
}()

// windowsDigitZeros are the zeros of the sets of decimal digits Windows
// compares as numbers, in code point order (ASCII, Arabic-Indic, the Indic
// scripts, Thai, Lao, Tibetan, Myanmar, Khmer, Mongolian, ..., full-width).
// Other decimal digits are other number characters.
var windowsDigitZeros = [...]rune{0x30, 0x660, 0x6f0, 0x966, 0x9e6, 0xa66, 0xae6, 0xb66, 0xbe6, 0xc66,
	0xce6, 0xd66, 0xe50, 0xed0, 0xf20, 0x1040, 0x1090, 0x17e0, 0x1810, 0x1946, 0x1bb0, 0x1c40, 0x1c50,
	0xa620, 0xa8d0, 0xa900, 0xaa50, 0xff10}

// windowsSpecials lists the characters Windows' word sort sets aside
// (hyphens, dashes, apostrophes), in the table's order. Control characters
// other than white space are set aside too, before these.
const windowsSpecials = "'ߴߵ-﹣⁻₋−‐֊‑‧⁃‒–︲—︱﹘―〜〰"

// symbolRanks maps the characters of windowsSymbols to their group and
// rank, and those of windowsSpecials to their rank (group 0).
var symbolRanks = func() map[rune][2]byte {
	m := map[rune][2]byte{}
	for group, chars := range windowsSymbols {
		rank := byte(1)
		for _, r := range chars {
			m[r] = [2]byte{byte(group), rank}
			rank++
		}
	}
	rank := byte(0x40) // after the control characters, ranked by code point
	for _, r := range windowsSpecials {
		m[r] = [2]byte{0, rank}
		rank++
	}
	for twin, r := range symbolTwins {
		m[twin] = m[r]
	}
	return m
}()

// symbolGroup is the group of a symbol missing from windowsSymbols, by its
// Unicode category (as most of the table's characters are placed).
func symbolGroup(r rune) byte {
	switch {
	case unicode.Is(unicode.Sm, r):
		return 8
	case unicode.Is(unicode.So, r):
		return 10
	}
	return 7
}

// charKind classifies the characters of a name.
type charKind uint8

const (
	charIgnored     charKind = iota // format characters (zero-width joiner, BOM, ...)
	charMark                        // combining marks: part of the letters before them
	charSpecial                     // set aside (windowsSpecials, control characters)
	charSymbol                      // spaces, punctuation, symbols
	charNumeral                     // windowsNumerals and hanNumerals
	charOtherNumber                 // the other number characters
	charText                        // letters and the rest
)

// charKindOf classifies a character that is not a digit of a number.
func charKindOf(r rune) charKind {
	if r < utf8.RuneSelf {
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z':
			return charText
		case r == '\'' || r == '-' || r < 0x20 && (r < '\t' || r > '\r') || r == 0x7f:
			return charSpecial
		}
		return charSymbol
	}
	if rk, ok := symbolRanks[r]; ok {
		if rk[0] == 0 {
			return charSpecial
		}
		return charSymbol
	}
	if _, ok := numeralRanks[r]; ok {
		return charNumeral
	}
	switch {
	case unicode.IsLetter(r), unicode.Is(unicode.Mc, r), unicode.Is(unicode.Nl, r):
		return charText
	case unicode.Is(unicode.Mn, r), unicode.Is(unicode.Me, r):
		return charMark
	case unicode.Is(unicode.Cf, r):
		return charIgnored
	case unicode.Is(unicode.Cc, r):
		return charSpecial
	case unicode.IsNumber(r): // also decimal digits outside windowsDigitZeros
		return charOtherNumber
	case unicode.IsPunct(r), unicode.IsSymbol(r), unicode.IsSpace(r):
		return charSymbol
	}
	return charText // private use and unassigned code points
}

// nameKeyer builds the keys of names. It orders letters with CLDR's
// Chinese collation (pinyin) at the primary level, so case, accents and
// width are ignored. It is not safe for concurrent use.
type nameKeyer struct {
	coll  *collate.Collator
	buf   collate.Buffer
	key   []byte // the key being built
	run   []byte // the letters of the current run of text
	spec  []byte // the specials of the current name
	chars int    // characters compared first so far, for the place of the specials
}

// nameKeyers pools keyers: a Collator is not safe for concurrent use.
var nameKeyers = sync.Pool{New: func() any {
	return &nameKeyer{coll: collate.New(language.Chinese, collate.IgnoreCase, collate.IgnoreDiacritics,
		collate.IgnoreWidth)}
}}

// weights returns the primary collation weights of s, valid until the next
// call.
func (k *nameKeyer) weights(s []byte) []byte {
	k.buf.Reset()
	return k.coll.Key(&k.buf, s)
}

// ImageSortKey returns the sort key of the image at rel, a slash-separated
// path relative to the dataset folder (see "Image order").
func ImageSortKey(rel string) []byte {
	k := nameKeyers.Get().(*nameKeyer)
	defer nameKeyers.Put(k)
	key := make([]byte, 0, 4*len(rel)+8)
	for {
		seg, rest, folder := strings.Cut(rel, "/")
		if folder {
			key = append(key, keyFolder)
		} else {
			key = append(key, keyFile)
		}
		key = k.appendNameKey(key, seg)
		if !folder {
			break
		}
		rel = rest
	}
	if len(key) > maxSortKeyLen {
		key = key[:maxSortKeyLen]
	}
	return key
}

// appendNameKey appends the key of one file or folder name.
func (k *nameKeyer) appendNameKey(key []byte, name string) []byte {
	k.key, k.run, k.spec, k.chars = key, k.run[:0], k.spec[:0], 0
	for i := 0; i < len(name); {
		if r, _ := utf8.DecodeRuneInString(name[i:]); isDigit(r) {
			k.flushText()
			i += k.appendNumber(name[i:])
			continue
		}
		// Up to the next digit, full-width forms are folded. Digits are not:
		// Windows does not join "１" and "2" into one number.
		j := len(name)
		if d := strings.IndexFunc(name[i:], isDigit); d >= 0 {
			j = i + d
		}
		k.appendChars(width.Fold.String(name[i:j]))
		i = j
	}
	k.flushText()
	key = append(k.key, keyEnd)
	key = append(key, k.spec...)
	key = append(key, keyEnd)
	key = append(key, name...) // file names never contain NUL
	k.key = nil
	return append(key, keyEnd)
}

// appendChars appends the tokens of characters that are not digits.
func (k *nameKeyer) appendChars(s string) {
	for i, r := range s {
		if r == utf8.RuneError {
			continue // U+FFFD, which Windows ignores (names are valid UTF-8)
		}
		size := utf8.RuneLen(r)
		switch kind := charKindOf(r); kind {
		case charText:
			k.run = append(k.run, s[i:i+size]...)
			k.chars++
		case charMark, charIgnored:
			if len(k.run) > 0 { // a combining mark changes the letter before it; elsewhere it is ignored
				k.run = append(k.run, s[i:i+size]...)
			}
		case charSpecial:
			// Set aside without ending a run of letters: "co-op" compares like "coop".
			k.spec = appendSpecial(k.spec, r, k.chars)
		case charSymbol:
			k.flushText()
			k.appendSymbol(r, s[i:i+size])
			k.chars++
		case charNumeral:
			k.flushText()
			k.key = append(k.key, tokNumeral, numeralRanks[r])
			k.chars++
		default: // charOtherNumber
			k.flushText()
			if w := k.weights([]byte(s[i : i+size])); len(w) > 0 {
				k.key = append(k.key, tokOtherNumber)
				k.key = appendEscaped(k.key, w)
			}
			k.chars++
		}
	}
}

// flushText appends the token of the current run of letters.
func (k *nameKeyer) flushText() {
	if len(k.run) == 0 {
		return
	}
	if w := k.weights(k.run); len(w) > 0 {
		k.key = append(k.key, tokText)
		k.key = appendEscaped(k.key, w)
	}
	k.run = k.run[:0]
}

// appendSymbol appends the token of a space, punctuation character or
// symbol.
func (k *nameKeyer) appendSymbol(r rune, s string) {
	if rk, ok := symbolRanks[r]; ok {
		k.key = append(k.key, tokSymbol, rk[0], rk[1])
		return
	}
	k.key = append(k.key, tokSymbol, symbolGroup(r), rankOther)
	k.key = appendEscaped(k.key, k.weights([]byte(s)))
}

// appendSpecial records a character set aside after chars characters: its
// place (an earlier one sorts later, as in Windows) and its rank.
func appendSpecial(spec []byte, r rune, chars int) []byte {
	rank := byte(r) // control characters
	if r == 0x7f {
		rank = 0x20
	} else if rk, ok := symbolRanks[r]; ok {
		rank = rk[1]
	} else if r >= utf8.RuneSelf {
		rank = 0x3f // C1 control characters
	}
	place := 0xFFFF - min(chars, 0xFFFF)
	return append(spec, special, byte(place>>8), byte(place), rank)
}

// maxNumberDigits is the longest run of digits compared as one number;
// Windows splits longer runs into numbers of this many digits.
const maxNumberDigits = 19

// appendNumber appends the token of the number s starts with (s starts
// with a digit): up to maxNumberDigits digits of one set of digits (Windows
// ends a number where ASCII and full-width digits meet). The token holds
// the count of significant digits and their values, so a shorter number
// comes first and numbers of the same length compare digit by digit, then
// the leading zeros, more zeros first. It returns the length of the number
// in s.
func (k *nameKeyer) appendNumber(s string) int {
	k.key = append(k.key, tokNumber, 0)
	countAt, digits, zeros, end := len(k.key)-1, 0, 0, 0
	r, _ := utf8.DecodeRuneInString(s)
	zero := digitZero(r)
	for i, r := range s {
		if digits == maxNumberDigits || digitZero(r) != zero {
			break
		}
		if v := byte(r - zero); v == 0 && len(k.key) == countAt+1 {
			zeros++
		} else {
			k.key = append(k.key, v)
		}
		digits++
		end = i + utf8.RuneLen(r)
	}
	k.key[countAt] = byte(len(k.key) - countAt - 1)
	k.key = append(k.key, byte(0xFF-zeros))
	k.chars += digits
	return end
}

// digitZero returns the zero of the set of digits r belongs to (see
// windowsDigitZeros), or -1 if r is not a digit of a number.
func digitZero(r rune) rune {
	if r < utf8.RuneSelf {
		if '0' <= r && r <= '9' {
			return '0'
		}
		return -1
	}
	for _, zero := range windowsDigitZeros[1:] {
		if r < zero {
			break
		}
		if r <= zero+9 {
			return zero
		}
	}
	return -1
}

// isDigit reports whether r is a digit of a number.
func isDigit(r rune) bool {
	return digitZero(r) >= 0
}

// appendEscaped appends w so that it can be followed by more key bytes and
// still compare like w alone, a prefix first: 0x00 becomes 0x00 0x01, and
// 0x00 0x00 ends it.
func appendEscaped(key, w []byte) []byte {
	for _, b := range w {
		if b == 0 {
			key = append(key, 0, 1)
		} else {
			key = append(key, b)
		}
	}
	return append(key, 0, 0)
}

// sortKeyBatch is the number of rows refreshSortKeys updates per statement.
const sortKeyBatch = 2000

// refreshSortKeys brings the stored sort keys up to date before anything
// lists images: rows without a key (indexed before the column existed, or
// by an older version of the server) get one, and when the stored keys
// were made by another version of the order, all keys are recomputed.
func (s *Service) refreshSortKeys(ctx context.Context) error {
	var stored string
	err := s.db.QueryRow(ctx, "SELECT value->>'version' FROM app_settings WHERE key = $1",
		imageOrderSetting).Scan(&stored)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	all := stored != imageOrderVersion
	if all {
		s.log.Info("computing all image sort keys", "order_version", imageOrderVersion, "previous_version", stored)
	}
	start := time.Now()
	n, err := s.updateSortKeys(ctx, all)
	if err != nil {
		return err
	}
	if all {
		if _, err := s.db.Exec(ctx, `INSERT INTO app_settings (key, value) VALUES ($1, jsonb_build_object('version', $2::text))
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
			imageOrderSetting, imageOrderVersion); err != nil {
			return err
		}
	}
	if n > 0 || all {
		s.log.Info("image sort keys updated", "images", n, "order_version", imageOrderVersion,
			"previous_version", stored, "duration_ms", time.Since(start).Milliseconds())
	}
	return nil
}

// updateSortKeys computes the keys of the rows without one, or of all rows,
// in batches by ID. It returns the number of rows whose key changed.
func (s *Service) updateSortKeys(ctx context.Context, all bool) (int64, error) {
	sql := "SELECT id, rel_path FROM dataset_images WHERE sort_key IS NULL AND id > $1 ORDER BY id LIMIT $2"
	if all {
		sql = "SELECT id, rel_path FROM dataset_images WHERE id > $1 ORDER BY id LIMIT $2"
	}
	var changed, after int64
	for {
		rows, err := s.db.Query(ctx, sql, after, sortKeyBatch)
		if err != nil {
			return changed, err
		}
		var (
			ids  []int64
			keys [][]byte
			id   int64
			rel  string
		)
		_, err = pgx.ForEachRow(rows, []any{&id, &rel}, func() error {
			ids = append(ids, id)
			keys = append(keys, ImageSortKey(rel))
			return nil
		})
		if err != nil {
			return changed, err
		}
		if len(ids) == 0 {
			return changed, nil
		}
		tag, err := s.db.Exec(ctx, `UPDATE dataset_images d SET sort_key = u.key
			FROM unnest($1::bigint[], $2::bytea[]) AS u (id, key)
			WHERE d.id = u.id AND d.sort_key IS DISTINCT FROM u.key`, ids, keys)
		if err != nil {
			return changed, fmt.Errorf("update sort keys: %w", err)
		}
		changed += tag.RowsAffected()
		after = ids[len(ids)-1]
	}
}
