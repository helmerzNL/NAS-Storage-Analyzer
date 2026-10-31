// Package categorize maps file names to extensions and extensions to categories.
package categorize

import "strings"

// Category identifiers. They are stable database keys; display names are
// resolved in the UI layer.
const (
	Video      int16 = 1
	Audio      int16 = 2
	Images     int16 = 3
	Documents  int16 = 4
	Archives   int16 = 5
	Code       int16 = 6
	DiskImages int16 = 7
	Other      int16 = 8
)

// Category describes one row of the categories table.
type Category struct {
	ID  int16
	Key string
}

// Categories lists all built-in categories in display order.
var Categories = []Category{
	{Video, "video"},
	{Audio, "audio"},
	{Images, "images"},
	{Documents, "documents"},
	{Archives, "archives"},
	{Code, "code"},
	{DiskImages, "disk_images"},
	{Other, "other"},
}

// maxExtLen is the longest string still treated as a file extension. Longer
// suffixes ("report.final version for review") are sentences, not extensions.
const maxExtLen = 16

// Ext returns the lower-cased extension of name without the dot, or "" when
// the name has none. A leading dot (".bashrc") and a trailing dot ("file.")
// do not form an extension. Only the last component counts ("a.tar.gz" -> "gz").
func Ext(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 || i == len(name)-1 {
		return ""
	}
	ext := name[i+1:]
	if len(ext) > maxExtLen || strings.ContainsAny(ext, " \t") {
		return ""
	}
	return strings.ToLower(ext)
}

// Mapper resolves extensions to categories. It is immutable after creation
// and safe for concurrent use.
type Mapper struct {
	byExt map[string]int16
}

// NewMapper builds a Mapper from an extension -> category table. Keys are
// lower-cased defensively.
func NewMapper(m map[string]int16) *Mapper {
	byExt := make(map[string]int16, len(m))
	for ext, cat := range m {
		byExt[strings.ToLower(ext)] = cat
	}
	return &Mapper{byExt: byExt}
}

// DefaultMapper returns a Mapper over DefaultExtensions().
func DefaultMapper() *Mapper { return NewMapper(DefaultExtensions()) }

// Category returns the category of an already-extracted extension. Unknown
// and empty extensions map to Other.
func (m *Mapper) Category(ext string) int16 {
	if c, ok := m.byExt[ext]; ok {
		return c
	}
	return Other
}
