package categorize

import "strings"

var defaultExtensions = map[int16]string{
	Video: "mkv mp4 m4v avi mov wmv flv webm mpg mpeg m2ts mts ts vob ogv 3gp divx rmvb f4v mxf",
	Audio: "flac mp3 m4a aac ogg oga opus wav wma aiff aif alac ape dsf dff mka mid midi amr",
	Images: "jpg jpeg png gif bmp tif tiff webp heic heif avif svg ico psd " +
		// camera RAW formats
		"cr2 cr3 crw nef nrw arw srf sr2 orf rw2 raf dng pef srw x3f 3fr erf mef mrw kdc dcr raw rwl iiq",
	Documents: "pdf doc docx xls xlsx ppt pptx odt ods odp rtf txt md csv epub mobi azw3 pages numbers key tex djvu xps one msg eml",
	Archives:  "zip rar 7z tar gz tgz bz2 tbz2 xz txz zst lz lz4 lzma cab z arj ace cpio",
	Code: "c h cc cpp hpp cs java kt go rs py rb php js mjs ts tsx jsx sh bash zsh ps1 bat cmd sql json yaml yml toml xml html htm css scss less " +
		"swift pl lua r dart vue svelte ini cfg conf",
	DiskImages: "iso img bin cue nrg mdf mds dmg vhd vhdx vmdk vdi qcow2 wim toast ova ovf",
}

// DefaultExtensions returns the built-in extension -> category table. It is
// the seed for the extension_map table; edits made through the UI live in the
// database and are never overwritten by the seed.
func DefaultExtensions() map[string]int16 {
	out := make(map[string]int16, 256)
	// Insertion order matters for extensions listed under two categories: the
	// first category in this slice wins ("ts" is video, "bin" is a disk image).
	for _, cat := range []int16{Video, Audio, Images, Documents, Archives, DiskImages, Code} {
		for _, ext := range strings.Fields(defaultExtensions[cat]) {
			if _, taken := out[ext]; !taken {
				out[ext] = cat
			}
		}
	}
	return out
}
