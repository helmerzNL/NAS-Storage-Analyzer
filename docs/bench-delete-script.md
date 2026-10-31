# Measurement: the lookup behind the delete script

Measured on 2026-10-31 with `nasabench script`. AGENTS.md section 6 asks for a re-run of `nasabench` when the query layer
changes; this change adds one query, `query.FilesForScript`, behind `POST /api/v1/shares/{share}/delete-script`. That
endpoint is Admin-only and a POST, so it is not one of the GET scenarios of `nasabench queries`; the new `nasabench script`
calls the query the way the handler does. The existing `queries` scenarios do not touch the changed code.

## What the query does

For up to 2,000 selected file ids it reads the files (primary key), resolves their folder paths (cached by folder id), and
decides which of them have a **case twin**, because a client that ignores case (SMB from Windows, a Mac) cannot tell
`Foo.txt` from `foo.txt`:

- **Files**: one pass over the rows of the selected files' folders, keeping only the rows whose lower-cased name is one of the
  selected lower-cased names (`dir_id = ANY(...) AND lower(name) = ANY(...)`). What comes back is the selection and its
  twins, not the folders.
- **Folders above the files**: the folder names under the parents of those folders, grouped in the database, only the
  groups with more than one member come back (the `dirs` table, about 5% of the size of `files`).

## Dataset and environment

| | |
|---|---|
| Files / folders | 2,000,000 / 40,000 (`nasabench seed -files 2000000 -dirs 40000`), 685 MB `files` with indexes |
| Database | PostgreSQL 16.15, `shared_buffers` 128 MB, `random_page_cost` 4 (the API session sets 2.0), 4 CPUs, local SSD |
| Method | `nasabench script -runs 20`: one first call, 20 warm calls; p50 / p95 / max of the warm calls |

## Result

| Selection (2,000 files) | First | p50 | p95 | max |
|---|---|---|---|---|
| spread over the share, 1,869 folders | 240 ms | 176 ms | 234 ms | 234 ms |
| all in the widest folder (42,546 files in it) | 33 ms | 35 ms | 57 ms | 57 ms |
| the 2,000 largest files | 195 ms | 178 ms | 220 ms | 220 ms |

Budget: 500 ms. All within it.

The rows above are for **files laid out as a scan writes them**: a scan copies the files of a folder together, so the siblings
of a file sit in the same few pages. The seed does not do that (it spreads a folder's files over the whole table), so the
table was clustered by folder (`CLUSTER files_s1 USING files_s1_share_id_dir_id_idx`) before this run. On the seed's own
layout, the worst case for this query, the same selections take **about 1 s** (880–1020 ms for the first and third rows, 160–190
ms for the second), because the 1,869 folders then spread over 32,500 table pages that have to be read. A library that has
been through many incremental scans is in between. With the database on a slow disk that is the number to watch: the API's
statement timeout (10 s) answers 503 `query took too long` and the person tries a smaller selection.

## What the measurement changed

The first version grouped and counted the names of every entry in the folders (and, for the folders above, of the files next
to them too) and took **2.9–3.3 s** on the seed's layout: two statements of 1.1 and 1.6 s, with the grouping spilling to disk.
Two changes took it to about 1 s there and under 250 ms on a clustered table: only the rows whose name is one of the selected
names are returned instead of all names grouped, and the folders above the files are compared with folders only. The second
change drops the check "a file and a folder next to each other whose names differ only in case"; that case fails safe in the
scripts, because what they find is not a regular file of the size they expect and every script keeps such a thing.

## Repeat it

```bash
nasabench seed   -db "$DSN" -share bench -files 2000000 -dirs 40000
nasabench script -db "$DSN" -share bench -runs 20
```
