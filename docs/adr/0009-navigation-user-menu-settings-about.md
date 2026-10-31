# ADR-0009: The name in the top bar is a menu, Settings are the application's, and About holds the version and the updates

- Status: Accepted
- Date: 2026-10-31
- Decision owners: helmerzNL (owner)

## Context

The top bar had the user name as a link, a separate *Sign out* button, and one *Settings* entry whose screen mixed what belongs to the signed-in person (appearance and language,
security) with the application's own settings (shares, file types, backup, users, audit log, system). A viewer therefore had a *Settings* entry that held two personal parts. The version
and the update status were under Settings > System, between the factory reset and nothing else. The owner asked for the name to become a pull-down menu that also holds *Sign out*, for a
settings screen that Users and the audit log live in, and for an About screen with the version and the updates.

[`web-app-design.md`](https://github.com/Flux76HQ/App-Guidance/blob/main/shared/guidelines/web-app-design.md) groups settings parts by what they are about and hides what a person may not use;
baseline §20 says a build reports its version and its channel "where it reports its version (`/api/version`, the About screen)"; [`update-check.md`](https://github.com/Flux76HQ/App-Guidance/blob/main/shared/guidelines/update-check.md)
names *Settings > System* as the place of the update status.

## Decision

- **The user menu** (`UserMenu.svelte`) is the name in the top bar with a chevron. Its entries are *Appearance and language*, *Security*, *About* and *Sign out*, each with an icon and a label,
  44 px high. It is a **disclosure**, not an application menu: a button with `aria-expanded` and a panel of ordinary links and one button, so Tab works as everywhere. Escape closes it and returns
  the focus to the button; a click elsewhere, or Tab out of it, closes it; the arrow keys, Home and End walk the entries as a convenience. The open screen is marked by more than colour (a bar and weight),
  on the button when the person is in one of their own screens, and on the entry in the panel. On a phone the name collapses to the person icon (it stays the button's accessible name), because
  signing out lives in the menu and has to stay reachable.
- **Settings are the application's own** and belong to administrators: *Data* (shares, file types, backup) and *Administration* (users, audit log, system). The top bar's *Settings* entry is shown to
  administrators only, since a viewer has nothing left there. The routes stay (`/admin/...`), so links and bookmarks keep working. The same grouped menu (`SettingsLayout`) serves the person's own
  screens (`/account`, `/account/security`) with a `scope`, so the two screens look alike and the guideline's rules (groups, icons, the open part marked, a swipeable row on a phone) hold for both.
- **About** (`/about`, for everybody) shows the application, the version, the release channel with the **beta** label, the build, a link to the source, and credits. For administrators it also shows
  the update status that used to be under System: the installed and the latest version, when it was last checked, *Check now*, the link to the release notes, how to update and what the check sends.
  A viewer is told that administrators see whether a newer release exists, and the server still refuses the update routes to anyone else.
- **Where the news shows:** the banner's *View* opens About, and the update dot sits on the user-menu button and on the *About* entry inside it. *Settings > System* keeps the factory reset and a
  pointer to About.
- **Deviation from `update-check.md`**, which names Settings > System: the owner asked for About, baseline §20 names the About screen for the version, and the guideline's own point (the status is
  where the administrator looks, and it only announces) is kept. The documents of this repository say About.

## Consequences

- A viewer has no Settings entry and no update information; they reach their own screens, About and Sign out from the menu.
- *Sign out* needs two clicks (open the menu, choose it). That is the price of one place for everything that is the person's.
- The menu is tested in a browser: its entries, the 44 px targets, Escape and focus, outside click, Tab out, the arrow keys, the open-screen marking, the phone width, and the update dot.
