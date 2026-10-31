package delscript

// Lang is the language of the text a script prints.
type Lang string

const (
	LangNL Lang = "nl"
	LangEN Lang = "en"
)

// messages is everything a script says. All of it is plain ASCII without the characters that mean something to
// cmd.exe, PowerShell or sh inside a quoted string (% & | < > ^ ( ) " ' $ `), so each renderer can embed it as it is.
// Placeholders do not exist: where a number belongs, the text is split in parts and the renderer puts the number
// between them in the way its language prints a variable.
type messages struct {
	// Lines of the summary, built by Go: these may use fmt verbs, and they are data, never code.
	title, share, root, scan, generated, files, size, hardlinks, folders, skippedHead, skippedMore, skippedLine string
	reason                                                                                                      map[Reason]string

	// Code-region strings. These must stay free of parentheses too: cmd closes a block at the first one.
	confirm    [2]string // "Type DELETE " n " to confirm: "
	cancelled  string
	incomplete [3]string // "... (expected " e " files, found " f "). Nothing was changed."
	notOpen    string
	noStat     string
	wrongRoot  string
	found      [3]string // "Found under the base path: " x " of " n "."
	done       [5]string // "Done: " d " deleted, " k " kept, " g " already gone, " f " failed."

	deleted, gone, changed, notRegular, failed string
}

var textEN = messages{
	title:       "NAS Storage Analyzer: delete script",
	share:       "Share:          %s",
	root:        "Base path:      %s",
	scan:        "Index of scan:  %s (scan %d)",
	generated:   "Generated:       %s",
	files:       "Files to delete: %d",
	size:        "Size (upper bound): %s",
	hardlinks:   "%d of these have other hard links: removing them may free no space.",
	folders:     "Per folder (largest first):",
	skippedHead: "NOT in this script (%d), see the reason per file:",
	skippedMore: "  ... and %d more",
	skippedLine: "  %s: %s",
	reason: map[Reason]string{
		ReasonGone:        "no longer in the index (a scan ran since you selected it)",
		ReasonAmbiguous:   "another file or folder differs only in upper or lower case",
		ReasonEncoding:    "the name is not valid text, so it cannot be addressed",
		ReasonControl:     "the name contains control or invisible characters",
		ReasonWindowsName: "Windows cannot address this name (use the sh script on the NAS)",
		ReasonReserved:    "Windows reserves this name (use the sh script on the NAS)",
		ReasonTooLong:     "the path is too long for this script",
		ReasonEmptyCmd:    "cmd cannot tell an empty file from a folder (use the PowerShell script)",
	},
	confirm:    [2]string{"Type DELETE ", " to confirm: "},
	cancelled:  "Cancelled. Nothing was changed.",
	incomplete: [3]string{"This script is incomplete or was changed: expected ", " files, found ", ". Nothing was changed."},
	notOpen:    "Cannot open the base path. Is it right, and is the share reachable? Nothing was changed.",
	noStat:     "This script needs a stat command that prints the size and time of a file. Nothing was changed.",
	wrongRoot:  "None of the files were found. Is the base path right? Nothing was changed.",
	found:      [3]string{"Found under the base path: ", " of ", "."},
	done:       [5]string{"Done: ", " deleted, ", " kept, ", " already gone, ", " failed."},
	deleted:    "deleted",
	gone:       "already gone",
	changed:    "changed since the scan, kept",
	notRegular: "not a regular file, kept",
	failed:     "FAILED",
}

var textNL = messages{
	title:       "NAS Storage Analyzer: verwijderscript",
	share:       "Share:          %s",
	root:        "Padbasis:       %s",
	scan:        "Index van scan: %s (scan %d)",
	generated:   "Gemaakt:         %s",
	files:       "Te verwijderen: %d",
	size:        "Grootte (bovengrens): %s",
	hardlinks:   "Van deze bestanden hebben er %d nog andere harde links: verwijderen geeft dan misschien geen ruimte.",
	folders:     "Per map (grootste eerst):",
	skippedHead: "NIET in dit script (%d), de reden staat per bestand:",
	skippedMore: "  ... en nog %d",
	skippedLine: "  %s: %s",
	reason: map[Reason]string{
		ReasonGone:        "staat niet meer in de index (er is een scan geweest sinds je het aanvinkte)",
		ReasonAmbiguous:   "een ander bestand of een andere map verschilt alleen in hoofdletters",
		ReasonEncoding:    "de naam is geen geldige tekst, dus niet te adresseren",
		ReasonControl:     "de naam bevat stuurtekens of onzichtbare tekens",
		ReasonWindowsName: "Windows kan deze naam niet adresseren (gebruik het sh-script op de NAS)",
		ReasonReserved:    "Windows reserveert deze naam (gebruik het sh-script op de NAS)",
		ReasonTooLong:     "het pad is te lang voor dit script",
		ReasonEmptyCmd:    "cmd kan een leeg bestand niet van een map onderscheiden (gebruik het PowerShell-script)",
	},
	confirm:    [2]string{"Typ DELETE ", " om te bevestigen: "},
	cancelled:  "Geannuleerd. Er is niets gewijzigd.",
	incomplete: [3]string{"Dit script is onvolledig of aangepast: verwacht ", " bestanden, gevonden ", ". Er is niets gewijzigd."},
	notOpen:    "De padbasis is niet te openen. Klopt hij, en is de share bereikbaar? Er is niets gewijzigd.",
	noStat:     "Dit script heeft een stat-opdracht nodig die grootte en tijd van een bestand geeft. Er is niets gewijzigd.",
	wrongRoot:  "Geen enkel bestand gevonden. Klopt de padbasis? Er is niets gewijzigd.",
	found:      [3]string{"Gevonden onder de padbasis: ", " van ", "."},
	done:       [5]string{"Klaar: ", " verwijderd, ", " bewaard, ", " bestond al niet meer, ", " mislukt."},
	deleted:    "verwijderd",
	gone:       "bestond al niet meer",
	changed:    "gewijzigd sinds de scan, bewaard",
	notRegular: "geen gewoon bestand, bewaard",
	failed:     "MISLUKT",
}

func textFor(l Lang) *messages {
	if l == LangNL {
		return &textNL
	}
	return &textEN
}
