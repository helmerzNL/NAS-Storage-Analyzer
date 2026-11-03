package delscript

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RootError is a base path that cannot be used. Its text is meant for the person who typed it.
type RootError struct{ Msg string }

func (e *RootError) Error() string { return e.Msg }

func rootErr(format string, a ...any) error { return &RootError{Msg: fmt.Sprintf(format, a...)} }

// NormalizeRoot checks the base path, "where the machine that runs the script sees the share", and returns it in
// one canonical form. For the Windows targets that is X:\folder or \\server\share\folder with backslashes; for sh
// it is an absolute path with slashes, and "/" for the root of the file system.
//
// The base path ends up in a script that deletes files, so it is validated as hard as a file name: control
// and invisible characters, quotes and the characters Windows cannot have are refused, and so are
// forms that mean something else than a plain folder (\\?\, relative paths, "." and "..").
func NormalizeRoot(t Target, raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", rootErr("the base path is empty")
	}
	for _, r := range s {
		switch {
		case r == utf8.RuneError || r == 0xfffd:
			return "", rootErr("the base path contains a character that is not valid text")
		case controlRune(r) || unicode.IsControl(r):
			return "", rootErr("the base path contains a control or invisible character")
		case r == '"' || r == '|':
			return "", rootErr("the base path may not contain %q", string(r))
		}
	}
	if t == TargetCmd && strings.ContainsRune(s, '%') {
		return "", rootErr("a base path with a percent sign cannot be used in a cmd script; use the PowerShell script")
	}
	if t.IsWindows() {
		return normalizeWindowsRoot(s)
	}
	return normalizePosixRoot(s)
}

func normalizePosixRoot(s string) (string, error) {
	if !strings.HasPrefix(s, "/") {
		return "", rootErr("the base path must be absolute, for example /mnt/user/Media or /Volumes/Media")
	}
	var parts []string
	for _, c := range strings.Split(s, "/") {
		switch c {
		case "":
		case ".", "..":
			return "", rootErr("the base path may not contain . or ..")
		default:
			parts = append(parts, c)
		}
	}
	return "/" + strings.Join(parts, "/"), nil
}

func normalizeWindowsRoot(s string) (string, error) {
	s = strings.ReplaceAll(s, "/", `\`)
	if strings.HasPrefix(s, `\\?\`) || strings.HasPrefix(s, `\\.\`) {
		return "", rootErr(`a base path that starts with \\?\ or \\.\ is not supported; use X:\folder or \\server\share`)
	}
	var prefix string
	var rest string
	switch {
	case len(s) >= 2 && s[1] == ':' && isASCIILetter(s[0]):
		prefix, rest = strings.ToUpper(s[:1])+`:`, s[2:]
		if rest != "" && !strings.HasPrefix(rest, `\`) {
			return "", rootErr(`a drive base path must look like Z:\ or Z:\folder`)
		}
	case strings.HasPrefix(s, `\\`):
		prefix, rest = `\\`, s[2:]
	default:
		return "", rootErr(`the base path must look like \\server\share, \\server\share\folder or Z:\folder`)
	}
	var parts []string
	for _, c := range strings.Split(rest, `\`) {
		switch c {
		case "":
		case ".", "..":
			return "", rootErr("the base path may not contain . or ..")
		default:
			if strings.ContainsAny(c, `<>:*?`) {
				return "", rootErr("the base path contains a character Windows does not allow in a name")
			}
			if strings.HasSuffix(c, " ") || strings.HasSuffix(c, ".") {
				return "", rootErr("a folder name in the base path ends in a space or a dot")
			}
			parts = append(parts, c)
		}
	}
	if prefix == `\\` {
		if len(parts) < 2 {
			return "", rootErr(`a network base path needs a server and a share: \\server\share`)
		}
		return `\\` + strings.Join(parts, `\`), nil
	}
	if len(parts) == 0 {
		return prefix + `\`, nil
	}
	return prefix + `\` + strings.Join(parts, `\`), nil
}

func isASCIILetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
