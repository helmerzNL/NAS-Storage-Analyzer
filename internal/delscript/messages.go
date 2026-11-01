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
	// Only in a script with folders: what the number to type is made of, and what a folder means.
	items, selFiles, selFolders, folderRule, folderHead, folderLine, covered string
	reason                                                                   map[Reason]string

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
	// The simple script: comment lines at its top, which are text for a person and are never run (see renderSimple).
	simpleTitle, simpleCount string
	simpleNote               [4]string

	// A folder that is kept: it is not a plain folder (a link, or a file now), it holds a link or a special file, or it holds more than at the scan.
	notFolder, hasLinks, grown string
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
	items:       "Items to delete: %d (files and folders, everything inside a selected folder included)",
	selFiles:    "  Files you selected: %d",
	selFolders:  "  Folders you selected: %d, with %d files and %d folders inside them at the scan",
	folderRule:  "A selected folder is deleted with EVERYTHING in it. It is kept, and reported, when it now holds more than at the scan, or a link.",
	folderHead:  "Folders to delete with everything in them (most files first):",
	folderLine:  "  %7d files %6d folders %10s  %s",
	covered:     "%d selected files or folders lie inside a selected folder and go with it.",
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
		ReasonRoot:        "this is the share itself, which is never deleted",
		ReasonFolderCmd:   "cmd cannot check what is in a folder (use the PowerShell script, or sh on the NAS)",
		ReasonCmdChars:    "cmd could read a character of this name as a variable (use PowerShell, or sh on the NAS)",
	},
	simpleTitle: "NAS Storage Analyzer: simple delete script",
	simpleCount: "Commands: %d (files: %d, folders: %d). Index of scan: %s (scan %d).",
	simpleNote: [4]string{
		"One command per file or folder. A folder is deleted with everything in it.",
		"Nothing is asked and nothing is checked: no summary, no confirmation, and no check that anything is still as it was at the scan.",
		"Pasting these lines into a terminal runs every one of them at once. Paste only the lines you want, or delete the others first.",
		"A link in a folder is not looked for. The advanced script does that.",
	},
	confirm:    [2]string{"Type DELETE ", " to confirm: "},
	cancelled:  "Cancelled. Nothing was changed.",
	incomplete: [3]string{"This script is incomplete or was changed: expected ", " entries, found ", ". Nothing was changed."},
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
	notFolder:  "not a plain folder, kept",
	hasLinks:   "holds links or special files, kept",
	grown:      "holds more than at the scan, kept",
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
	items:       "Te verwijderen onderdelen: %d (bestanden en mappen, alles in een gekozen map inbegrepen)",
	selFiles:    "  Bestanden die je koos: %d",
	selFolders:  "  Mappen die je koos: %d, met %d bestanden en %d mappen erin bij de scan",
	folderRule:  "Een gekozen map wordt verwijderd met ALLES erin. Hij blijft staan, en wordt gemeld, als hij nu meer bevat dan bij de scan, of een koppeling.",
	folderHead:  "Mappen die met alles erin worden verwijderd (meeste bestanden eerst):",
	folderLine:  "  %7d bestanden %6d mappen %10s  %s",
	covered:     "%d gekozen bestanden of mappen liggen in een gekozen map en gaan daarmee mee.",
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
		ReasonRoot:        "dit is de share zelf, die wordt nooit verwijderd",
		ReasonFolderCmd:   "cmd kan niet controleren wat er in een map staat (gebruik het PowerShell-script, of sh op de NAS)",
		ReasonCmdChars:    "cmd kan een teken van deze naam als variabele lezen (gebruik PowerShell, of sh op de NAS)",
	},
	simpleTitle: "NAS Storage Analyzer: eenvoudig verwijderscript",
	simpleCount: "Opdrachten: %d (bestanden: %d, mappen: %d). Index van scan: %s (scan %d).",
	simpleNote: [4]string{
		"Een opdracht per bestand of map. Een map wordt verwijderd met alles erin.",
		"Er wordt niets gevraagd en niets gecontroleerd: geen samenvatting, geen bevestiging, en geen controle of iets nog is zoals bij de scan.",
		"Plak je deze regels in een terminal, dan worden ze allemaal meteen uitgevoerd. Plak alleen de regels die je wilt, of haal de andere eerst weg.",
		"Een koppeling in een map wordt niet gezocht. Het geavanceerde script doet dat wel.",
	},
	confirm:    [2]string{"Typ DELETE ", " om te bevestigen: "},
	cancelled:  "Geannuleerd. Er is niets gewijzigd.",
	incomplete: [3]string{"Dit script is onvolledig of aangepast: verwacht ", " onderdelen, gevonden ", ". Er is niets gewijzigd."},
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
	notFolder:  "geen gewone map, bewaard",
	hasLinks:   "bevat koppelingen of speciale bestanden, bewaard",
	grown:      "bevat meer dan bij de scan, bewaard",
}

func textFor(l Lang) *messages {
	if l == LangNL {
		return &textNL
	}
	return &textEN
}
