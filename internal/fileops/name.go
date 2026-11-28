package fileops

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

func SafeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '/' || r == '\\' || strings.ContainsRune("<>:\"|?*", r) || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) {
			return '_'
		}
		return r
	}, strings.ToValidUTF8(name, "_"))
	for len(name) > 128 {
		_, n := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-n]
	}
	name = strings.Trim(name, " .")
	base := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if base == "" || base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9') {
		name = "_" + name
	}
	for len(name) > 128 {
		_, n := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-n]
	}
	return name
}
