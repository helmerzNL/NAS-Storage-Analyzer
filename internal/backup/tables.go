package backup

// Coverage says whether a table is in the backup file and why. Every table of the schema is listed, and a test fails when a migration adds one
// that is not: "a new table that holds user data belongs in the backup and in its round-trip test, in the same change" (backup-restore.md)
// cannot be left to memory.
type Coverage struct {
	InFile bool
	Why    string
}

// Tables classifies every table of the schema (partitions of dirs, files, dir_category_agg and share_ext_agg are covered by their parent).
var Tables = map[string]Coverage{
	"shares":        {true, "what the owner entered: where to scan and how (the SMB password only as the encrypted blob)"},
	"extension_map": {true, "the file types the owner edited"},
	"categories":    {false, "built in, the same on every installation; file types refer to them by key"},
	"users":         {false, "accounts, roles and credentials are never in the file; only each person's interface preferences are (users.prefs)"},

	"recovery_codes":       {false, "unlocks the app"},
	"webauthn_credentials": {false, "unlocks the app"},
	"webauthn_ceremonies":  {false, "short-lived sign-in state"},
	"sessions":             {false, "unlocks the app"},
	"login_failures":       {false, "state of the sign-in lockout"},
	"invitations":          {false, "unlocks the app: a link that creates or resets an account"},
	"audit_log":            {false, "a record of what happened here, not something the owner entered"},
	"app_settings":         {false, "not written by the application at present; when it is, decide here whether it belongs in the file"},

	"scans":             {false, "derived from the files on the NAS: scan again after a restore"},
	"scan_errors":       {false, "derived from a scan"},
	"jobs":              {false, "work in progress"},
	"dirs":              {false, "scan results"},
	"files":             {false, "scan results"},
	"dir_category_agg":  {false, "scan results"},
	"share_ext_agg":     {false, "scan results"},
	"snapshots":         {false, "scan history"},
	"snapshot_category": {false, "scan history"},
	"snapshot_ext":      {false, "scan history"},
	"snapshot_dir":      {false, "scan history"},

	"backup_stages":     {false, "a file waiting for confirmation: work in progress"},
	"backup_jobs":       {false, "work in progress; a restore never touches it, so the job's own row survives the swap it performs"},
	"schema_migrations": {false, "the version of the schema, not data"},
}

// ShareColumns classifies every column of the shares table: the ones in the file are what the owner entered, the others are state the
// application keeps. A test fails when a migration adds a column that is not listed here.
var ShareColumns = map[string]Coverage{
	"name": {true, "entered"}, "kind": {true, "entered"}, "config": {true, "entered: path, server, export, share, user name, domain, version, mount options, client paths"},
	"excludes": {true, "entered"}, "workers": {true, "entered"}, "max_ops_per_sec": {true, "entered"}, "max_ops_day": {true, "entered"},
	"day_start_hour": {true, "entered"}, "day_end_hour": {true, "entered"}, "cron": {true, "entered"}, "last_used_basis": {true, "entered"},
	"full_scan_every": {true, "entered"}, "retention_scans": {true, "entered"}, "retention_days": {true, "entered"}, "snapshot_depth": {true, "entered"},
	"secret_enc": {true, "only as the ciphertext the database holds, sealed with the key in the environment"},

	"id":           {false, "a number this installation hands out"},
	"last_scan_id": {false, "state: the scan the share's data comes from"},
	"next_run_at":  {false, "state: worked out again from cron when a share is restored"},
	"last_run_at":  {false, "state"},
	"created_at":   {false, "state"},
}
