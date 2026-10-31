package categorize

import "testing"

func TestExt(t *testing.T) {
	cases := map[string]string{
		"movie.MKV":                        "mkv",
		"IMG_0001.CR2":                     "cr2",
		"archive.tar.gz":                   "gz",
		"README":                           "",
		".bashrc":                          "",
		"trailing.":                        "",
		"":                                 "",
		"a.b c":                            "",
		"naïve café.FLAC":                  "flac",
		"x.averyveryverylongextensionname": "",
		"..hidden":                         "hidden",
	}
	for name, want := range cases {
		if got := Ext(name); got != want {
			t.Errorf("Ext(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestDefaultMapper(t *testing.T) {
	m := DefaultMapper()
	cases := map[string]int16{
		"mkv": Video, "m2ts": Video, "mp4": Video,
		"flac": Audio, "mp3": Audio,
		"cr2": Images, "nef": Images, "arw": Images, "dng": Images, "jpg": Images,
		"pdf": Documents, "xlsx": Documents,
		"zip": Archives, "7z": Archives, "gz": Archives,
		"go": Code, "py": Code,
		"iso": DiskImages, "vmdk": DiskImages, "qcow2": DiskImages,
		"": Other, "unknownext": Other,
	}
	for ext, want := range cases {
		if got := m.Category(ext); got != want {
			t.Errorf("Category(%q) = %d, want %d", ext, got, want)
		}
	}
}

func TestDefaultExtensionsAreLowercaseAndKnownCategories(t *testing.T) {
	known := map[int16]bool{}
	for _, c := range Categories {
		known[c.ID] = true
	}
	for ext, cat := range DefaultExtensions() {
		if ext != Ext("x."+ext) {
			t.Errorf("default extension %q is not in canonical form", ext)
		}
		if !known[cat] {
			t.Errorf("extension %q maps to unknown category %d", ext, cat)
		}
	}
}

func TestCustomMapperIsCaseInsensitive(t *testing.T) {
	m := NewMapper(map[string]int16{"FOO": Code})
	if got := m.Category("foo"); got != Code {
		t.Fatalf("got %d, want Code", got)
	}
}

func TestDuplicateExtensionFirstCategoryWins(t *testing.T) {
	// "ts" appears in both Video and Code lists; Video must win.
	if got := DefaultMapper().Category("ts"); got != Video {
		t.Fatalf("ts -> %d, want Video", got)
	}
}
