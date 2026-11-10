package notify

import (
	"fmt"
	"strings"
	"time"
)

// Kinds of event. The scan kinds are the audit actions the worker writes for the same outcomes.
const (
	KindFailed   = "scan.failed"
	KindEmpty    = "scan.empty"
	KindErrors   = "scan.errors"
	KindFinished = "scan.finished"
	KindTest     = "test"
)

// Event is what happened. It holds the outcome of a scan and nothing about what the scan found: no file name, no folder listing.
type Event struct {
	Kind    string
	Share   string
	ShareID int64
	ScanID  int64
	JobID   int64
	Trigger string // manual or schedule
	Files   int64
	Dirs    int64
	Bytes   int64
	Errors  int64  // folders or files that could not be read
	Error   string // why a scan failed, in the worker's words
	At      time.Time
}

// Message is what is sent: the body of a json target as it is, and for a text target the title and priority as headers around the
// message. The field names are chosen so that Gotify takes the object unchanged (title, message, priority) and Home Assistant gets
// everything under trigger.json.
type Message struct {
	App      string    `json:"app"`
	Event    string    `json:"event"`
	Title    string    `json:"title"`
	Message  string    `json:"message"`
	Priority int       `json:"priority"` // 1-10: 8 needs attention now, 5 worth a look, 2 nothing to do
	Share    string    `json:"share,omitempty"`
	ShareID  int64     `json:"share_id,omitempty"`
	ScanID   int64     `json:"scan_id,omitempty"`
	JobID    int64     `json:"job_id,omitempty"`
	Trigger  string    `json:"trigger,omitempty"`
	Files    *int64    `json:"files,omitempty"`
	Dirs     *int64    `json:"dirs,omitempty"`
	Bytes    *int64    `json:"bytes,omitempty"`
	Errors   *int64    `json:"errors,omitempty"`
	Error    string    `json:"error,omitempty"`
	At       time.Time `json:"at"`
}

// AppName is how a message says who it is from.
const AppName = "NASSA"

type texts struct {
	failed, failedMsg, empty, emptyMsg, errors, errorsMsg, finished, finishedMsg, test, testMsg string
}

var languages = map[string]texts{
	"en": {
		failed: "scan of %s failed", failedMsg: "The scan of %s failed: %s",
		empty: "scan of %s found nothing", emptyMsg: "The scan of %s finished and found no files. Is the share still mounted, and does its path point at the right folder?",
		errors: "scan of %s could not read everything", errorsMsg: "The scan of %s finished, but %d folders or files could not be read. The audit log names the folders.",
		finished: "scan of %s finished", finishedMsg: "The scan of %s finished: %d files in %d folders, %s.",
		test: "test message", testMsg: "This message was sent with the Test button. Messages about scans arrive the same way.",
	},
	"nl": {
		failed: "scan van %s mislukt", failedMsg: "De scan van %s is mislukt: %s",
		empty: "scan van %s vond niets", emptyMsg: "De scan van %s is afgerond en vond geen bestanden. Is de share nog gekoppeld, en wijst het pad naar de juiste map?",
		errors: "scan van %s kon niet alles lezen", errorsMsg: "De scan van %s is afgerond, maar %d mappen of bestanden konden niet worden gelezen. Het auditlog noemt de mappen.",
		finished: "scan van %s klaar", finishedMsg: "De scan van %s is klaar: %d bestanden in %d mappen, %s.",
		test: "testmelding", testMsg: "Deze melding is verstuurd met de knop Test. Meldingen over scans komen op dezelfde manier aan.",
	},
}

// size writes a number of bytes the way the interface does by default (1024 steps), with a decimal comma in Dutch.
func size(n int64, lang string) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	v, i := float64(n), 0
	for v >= 1024 && i < len(units)-1 {
		v, i = v/1024, i+1
	}
	s := fmt.Sprintf("%.1f %s", v, units[i])
	if i == 0 {
		s = fmt.Sprintf("%d B", n)
	}
	if lang == "nl" {
		s = strings.Replace(s, ".", ",", 1)
	}
	return s
}

// oneLine keeps a reason readable in a notification: one line, not endless.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}

// compose writes the message for an event in a language (English when the language is not known).
func compose(ev Event, lang string) Message {
	t, ok := languages[lang]
	if !ok {
		t, lang = languages["en"], "en"
	}
	m := Message{App: AppName, Event: ev.Kind, Share: ev.Share, ShareID: ev.ShareID, ScanID: ev.ScanID, JobID: ev.JobID, Trigger: ev.Trigger, At: ev.At.UTC().Truncate(time.Second)}
	counts := func() { m.Files, m.Dirs, m.Bytes, m.Errors = &ev.Files, &ev.Dirs, &ev.Bytes, &ev.Errors }
	title := ""
	switch ev.Kind {
	case KindFailed:
		reason := oneLine(ev.Error, 500)
		title, m.Message, m.Priority, m.Error = fmt.Sprintf(t.failed, ev.Share), fmt.Sprintf(t.failedMsg, ev.Share, reason), 8, reason
	case KindEmpty:
		title, m.Message, m.Priority = fmt.Sprintf(t.empty, ev.Share), fmt.Sprintf(t.emptyMsg, ev.Share), 8
		counts()
	case KindErrors:
		title, m.Message, m.Priority = fmt.Sprintf(t.errors, ev.Share), fmt.Sprintf(t.errorsMsg, ev.Share, ev.Errors), 5
		counts()
	case KindFinished:
		title, m.Message, m.Priority = fmt.Sprintf(t.finished, ev.Share), fmt.Sprintf(t.finishedMsg, ev.Share, ev.Files, ev.Dirs, size(ev.Bytes, lang)), 2
		counts()
	default:
		m.Event = KindTest
		title, m.Message, m.Priority = t.test, t.testMsg, 5
	}
	m.Title = AppName + ": " + title
	return m
}
