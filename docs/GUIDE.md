# Mailday user guide

Mailday (`md`) is a terminal mail and calendar client written in Go, and this guide lists what it does, which keys do it and how to configure it. The README has the overview and the screenshots.

## What Mailday reads and writes

- **Mail** comes from Maildirs that mbsync (or any other tool) keeps current. Mailday looks in `~/Mail` and `~/.local/share/mail` (the mutt-wizard layout), one directory per account, and reads `Inbox` or `INBOX` in each. `MAILDAY_MAIL_ROOT` (or `-mail-root`) replaces those two roots with a path list of your own.
- **Boxes** are the folders of an account. Folders whose names start with `@` (such as `@Reply`, `@Waiting` or `@Clients`) are tag folders, and a tool of yours can file mail into them. Mailday only needs them to exist as Maildir folders. Next to them it shows a Sent box (the newest 300 sent messages per account, listing the recipient, with the Sent folder spelling that holds the newest mail winning) and an Archive box (the newest 300 archived messages per account). Gmail's All Mail copies of messages already shown in another box are left out.
- **Calendars** come from ICS files in `~/.local/share/mailday/calendars` (written by `mailday-calsync`, or put there by hand), from a Microsoft 365 calendar over EWS (`mailday-exchange`), and from Evolution Data Server's local calendar cache. Calendar databases are opened read-only.
- **Sending** goes through msmtp.
- **NeoMutt** stays available for attachments and anything else the interface does not do (`o` opens the mailbox in it).

The full-screen Mail and Calendar interface is adapted from [basecamp/hey-cli](https://github.com/basecamp/hey-cli/tree/main/internal/tui). `THIRD_PARTY_NOTICES.md` has the upstream license.

Other parts of the mail side:

- **Address book.** Recipient autocompletion draws on every Maildir header (`internal/contacts`). People you write to rank first, then people who write to you, with no-reply senders and mailing lists left out. The list is cached in `~/.cache/mailday/contacts.json` and rebuilt in the background (well under a second) when it is older than 15 minutes, when the composer opens and it is older than a minute, and after sending.
- **Signatures** are set per identity in `config.toml` (see Configuration). A full signature goes under new mail and forwards, a short one under replies, above the quote. They swap along when From changes and stay editable per mail. The HTML part styles them with a short rule, the name in weight, the rest small and grey, and web addresses linked.
- **Attachments.** Typing words in the attach field searches your files. "conf abstract" finds `Projects/abstract_conf.pdf`. Every word must appear in the path, file-name matches and recent files rank first, and the search runs through Spotlight on macOS and fd on Linux over `~/Documents` first, then Downloads and Desktop (the rest of home is not searched). Typing a path (`~/`, `/`, `./`) completes folder by folder instead. `MAILDAY_FILE_SEARCH=walk` forces the plain directory walk. Forwards carry the original's attachments, and messages with files go out as multipart/mixed with an 18 MB cap.
- **Writing in Markdown.** The draft is a `.md` file with a front-matter header block. It goes through msmtp as multipart/alternative, with the Markdown as text/plain and goldmark's HTML as text/html. Replies thread with In-Reply-To and References and flag the original answered. Drafts stay in `~/.local/share/mailday/drafts` until sent. The composer saves its draft every 20 seconds while it changes, and a reply or new message closed untouched leaves no draft. `y` waits 10 seconds before sending (`MAILDAY_SEND_DELAY`, 0 sends at once), and `u` takes the message back.
- **Reader.** HTML mail becomes Markdown and renders through glamour in ANSI colours (`internal/htmlutil`, `internal/markdown`, adapted from hey-cli, MIT). Plain mail gets quote bars and a muted signature, quoted history collapses, and Microsoft Safe Links are unwrapped.

Nobody needs to press sync. `mailday-syncd` keeps the Maildirs current and the interface watches them on disk, so new mail and moves made on a phone show up within seconds. The footer says `live` while every account is watched.

## Configuration

Mailday reads `~/.config/mailday/config.toml` (`$XDG_CONFIG_HOME/mailday/config.toml` when that variable is set, or the path in `MAILDAY_CONFIG`). Every key is optional. A missing file is an empty config, and a file that does not parse is reported and ignored.

```toml
name = "Ada Example"                    # default display name for identities
shared_dir = "~/Sync/mailday"           # snooze list, groups, leases (default ~/.local/share/mailday)

[[identity]]
accounts = ["gmail", "work.example"]    # account directories under the mail root, or their domains
name = "Ada Example"
address = "ada@example.com"
aliases = ["ada@work.example"]          # other addresses that arrive as this identity
signature = ["Ada Example", "Example Ltd", "https://example.com"]
short_signature = ["Ada"]
save_sent = false                       # file a local Sent copy

[reply_judge]
enabled = false

[calendar]
feeds = [{ name = "Holidays", url = "https://example.com/holidays.ics" }]
google_script = "~/bin/gcal.py"
google_own = ["ada@example.com"]
home_zone = "Europe/Berlin"

[exchange]
calendar = "Outlook"
user = "ada@work.example"
token = "~/.config/mailday/ews-token.gpg"
client_id = ""
ews_url = ""
mailbox_tz = ""

[sync]
post_sync = "notmuch new --quiet"

[mail.box_keys]
"@Clients" = "c"
"@Hiring" = "h"
"@Travel" = "t"
```

- **name** is the display name for any identity that sets none.
- **shared_dir** holds `snooze.json`, `groups.txt` and `leases/`, the state worth sharing between machines. Point it into a synced folder such as Syncthing or Dropbox. The default is `~/.local/share/mailday` (`$XDG_DATA_HOME/mailday` when set).
- **[[identity]]** blocks say who you send as. `accounts` names the Mailday accounts (a directory under the mail root, or the domain) the identity belongs to. Every identity needs an `address`. `save_sent` files a local Sent copy and should stay off for Gmail and Exchange Online, which keep their own copy of SMTP submissions. Without any identity, Mailday sends as each msmtp account's from address and adds no signature.
- **[reply_judge]** switches on the reply judge described under Keys. It is off unless `enabled = true` and the Claude CLI is installed.
- **[calendar]** feeds, the Google script and the home time zone are covered under Calendar sources and Travelling.
- **[exchange]** sets up the Microsoft 365 calendar (see Calendar sources). Exchange stays off while `user` is empty.
- **[sync]** `post_sync` is a shell command `mailday-syncd` runs after syncs. Without it the daemon runs `notmuch new --quiet` when notmuch is installed.
- **[mail.box_keys]** gives a folder a fixed key in the box and filing pickers (see Keys).

## Install and check

```bash
mise install
make check
make install
```

`make install` writes `mailday` and `mailday-syncd` to `~/.local/bin`, with the helper scripts `mailday-calsync`, `mailday-exchange` and `mailday-calendar`. Add one of the following to run the sync daemon at login.

```bash
make install-syncd     # Linux: a systemd user unit; turn off any mbsync timer you had
make install-launchd   # macOS: launchd agent local.mailday.syncd
```

Check the setup without printing message or event text and without touching the network.

```bash
mailday --check
```

It prints a checklist, `✓` or `✗` with the fix beside each `✗`. The items are mail roots and accounts found, every account in `~/.mbsyncrc` against the Maildir accounts, `mbsync`, `neomutt`, `sqlite3`, `msmtp` (sending), `notmuch` (search), `mailday-syncd` running, `mailday-calendar` (calendar editing), `mailday-calsync`, the Exchange token, the Google login (invitations), `claude` (reply judge), the editor for `ctrl+o`, and the time zone against the home zone. The `key=value` lines that follow are for scripts. The exit status is non-zero only when the mail roots are missing.

Then start the interface.

```bash
mailday
```

## Live sync (`mailday-syncd`)

A small daemon in this repo decides when mbsync runs. mbsync stays the only sync engine, so its state files stay authoritative and nothing is downloaded twice.

- **Server to disk.** One IMAP IDLE connection per account watches `INBOX`, `@Reply` and `@Waiting` (`-idle` changes the list). When the server reports a change, only that mailbox is synced, in about a second. Every 30 seconds each connection is interrupted for a NOOP (`-refresh`). Gmail's IDLE often reports new mail late and flag changes never, but it answers a NOOP with everything pending, and the NOOP also proves the link is alive. Servers that drop idle connections early are detected and refreshed sooner.
- **Disk to screen.** Mailday watches every `cur/` and `new/` and reloads quietly after a burst settles (300 ms), keeping the selection and the status line. The selection follows a message by its Maildir key, so a rename (a flag changed elsewhere, mbsync adding a UID) does not move it. When the message open in the reader is archived or deleted elsewhere, the reader says so and refuses `d`, `a`, `f`, `m` and replies instead of acting on the neighbour.
- **Arrivals in view.** New inbox mail that arrives while Mailday is open shows in the status line with sender and subject (unless another message is showing), and the terminal title reads `Mailday (n)` with the unread inbox count.
- **Opening reads.** Opening a message marks it read, as Outlook does. `m` marks it unread again.
- **Sent.** Gmail and Exchange file their own copy of what Mailday sends. Mailday asks the daemon to fetch Sent 3 and 15 seconds after sending, so the copy shows up without a sweep.
- **Offline.** Without a network the footer says `offline`. Changes stay in the Maildirs and go out with the next successful sync. Failed syncs retry after 15 seconds and back off to 10 minutes.
- **Your changes to the server.** Reading, answering and sending tell the daemon which folders changed, and it runs `mbsync --push` for just those. Filing, archiving and deleting move the message on the server with IMAP MOVE (found by its Message-ID) and let mbsync download it in its new folder. Nothing large goes up, and Gmail keeps one message with its labels changed. When that cannot work (no Message-ID, no MOVE or UIDPLUS) the daemon pushes the old way, uploading Mailday's copy and expunging the old one.
- **One mbsync per account at a time.** Requests that arrive while mbsync runs are merged into the next run. A box locked by another mbsync (NeoMutt, say) is retried.
- **Every other folder.** One more connection per account runs STATUS every 30 seconds (`-poll`) on each synced mailbox that has no IDLE watch (Sent, Archive, Trash, the other tags). It syncs the mailbox whose message count, next UID, unread count or mod-sequence moved, so mail filed on a phone shows up within half a minute. Mail that enters or leaves a watched box also syncs that account's Archive and Trash at once.
- **Safety net.** Every account is synced in full at start and every 30 minutes (`-sweep`). After a suspend (the wall clock jumped ahead of the monotonic clock) every connection is reopened and everything is synced.
- **Notifications.** New inbox mail raises one desktop notification per sync with sender and subject (`-notify=false` turns it off). On Linux it has an Open button that starts Mailday on that message (`mailday --open PATH`). Notifications appear only on a machine someone is using, so not while the session is locked, and on macOS not after five minutes without keyboard or mouse. With two machines awake, the one you are at speaks up. On macOS they go through terminal-notifier, or osascript without it.
- **Calendar.** The daemon runs `mailday-calsync` (`-calendar`) at once when Mailday opens or asks for a full sync, every 3 minutes while Mailday is open (`-calendar-active`), every 15 otherwise (`-calendar-idle`), and after a resume. Mailday reloads the agenda as soon as the ICS files change. Google Meet and Zoom links from an event's conference data go to the top of its notes, and `J` in an event opens the meeting link.
- **Meeting reminders (Linux).** Ten minutes before a timed event (`-remind`, 0 turns it off) a notification offers Join (the Teams, Zoom or Meet link) and Open calendar (`mailday --calendar`). Calendars Evolution also holds are left to evolution-alarm-notify so Google events do not pop up twice. `-remind-all` takes them on too. Reminders do not exist on macOS.
- **After each sync.** The daemon runs `[sync].post_sync`, else `notmuch new --quiet` when notmuch is installed, so the `/` search finds mail as it arrives. `-post-sync` overrides both.
- **Running a job on one machine.** `mailday-syncd lease NAME 40m -- COMMAND` runs COMMAND unless another machine sharing `shared_dir` ran it within 40 minutes. The lease is a file in `leases/` in `shared_dir` (`MAILDAY_LEASE_DIR` moves it). Use it for jobs that two machines would otherwise run twice, such as a mail tagger.

Accounts, passwords (`PassCmd`) and the mailbox-to-channel mapping are read from `~/.mbsyncrc`. The daemon listens on the socket `$XDG_RUNTIME_DIR/mailday-syncd.sock` (`~/Library/Caches/mailday/syncd.sock` on macOS, `MAILDAY_SYNCD_SOCKET` overrides both).

```bash
make install-syncd        # installs and starts the user service
mailday-syncd status      # accounts, watched mailboxes, last sync, errors
mailday-syncd sync        # everything now (-wait to block, PATH... for some folders)
journalctl --user -u mailday-syncd -f
```

`s` in the interface asks the daemon for a full sync and waits for it. Without the daemon it runs `mbsync -a`.

On macOS, `make install-launchd` installs the agent `local.mailday.syncd` and logs to `~/Library/Logs/mailday-syncd.log`. The agent sets `SASL_PATH` so Homebrew's isync finds the XOAUTH2 plugin when Homebrew's cyrus-sasl is present. To stop a previous mbsync launchd job from syncing in parallel, unload it yourself.

## Mail and boxes

Boxes are grouped, which is presentation only.

- **Now** holds Inbox, Reply and Waiting.
- **Projects** holds every other `@` tag folder.
- **Mail** holds Drafts, Sent, Archive and Trash, where they exist.

Terminals 130 columns or wider get a sidebar of about 24 columns beside the list, showing the three sections with unread counts (dim at none, no number for Sent, Archive, Drafts and Trash). Projects are sorted by their newest message and tags without mail are hidden. `\` hides the sidebar for the session, and the box row of the header is then left out. Narrower terminals get one short row instead, with the Now boxes and counts, `Projects ▾ 4` (unread across projects, or `Projects ▾ Clients` while a project is open) and the Mail boxes.

`g p` opens the project picker, which lists every tag with its letter, unread count and last date. A letter jumps (as `g` plus the letter does). `/` or any key that is not a project letter starts a filter (case-insensitive substring), up and down move, `enter` opens and `esc` closes.

The list shows the time for today, the weekday for earlier days of this week, `2 Jan` for this year and `2 Jan 2006` before that. In `@Reply` and `@Waiting` it shows how long the message has waited (`9d`).

After `a a`, `f`, `d` or `m` in a message, the next message opens instead of the list (the last one returns to it). `MAILDAY_AUTO_ADVANCE=off` turns that off. `u` undoes from the message too.

### Filing keys

`f` followed by a letter files the selected message into a box. These letters are built in.

| Key | Box |
|---|---|
| `f i` | Inbox |
| `f r` | @Reply |
| `f w` | @Waiting |
| `f x` | Archive |

Every other box gets the first free letter of its name, so `@Clients` takes `c`, and `@Hiring` takes `h`. `f` and `q` stay free to close the picker. When a letter is taken, the next letter of the name is tried, then a digit from 1 to 9. To fix a box on one key, whatever the order of the others, set it in config.

```toml
[mail.box_keys]
"@Clients" = "c"
"@Travel" = "t"
```

The same letters work after `g` to go to a box. `@Snoozed` is not offered by `f` (see Snooze).

### Search and sending checks

`/` searches the loaded mail by sender, subject, address or account as you type, and every message, bodies too, through notmuch a moment later. `from:ada`, `subject:budget` and `date:2025..` work. Hits from Trash and Spam come last.

Before the send countdown, `y` checks the draft. It flags a missing subject, a mention of an attachment with no file attached, and more than 8 people in To and Cc. The attachment words are attach, attached, attachment, bijlage, bijgevoegd, angehängt and 첨부, and quoted lines and the signature do not count. The check says so once, and a second `y` sends anyway. The preview shows `to 2 · cc 31` and wraps long recipient lines.

## Keys

| Key | Action |
|---|---|
| `j` / `k`, up / down | Move through messages or events |
| `tab` | Switch between Mail and Calendar |
| `enter` | Read a message (marks it read) or inspect an event |
| left / right | Switch mail accounts or calendar spans |
| `b` / `B` | Next or previous box, in the order of the sidebar (Now, Projects, Mail) |
| `\` | Show or hide the box sidebar for this session (terminals 130 columns or wider) |
| `g` + key | Go to a box with the same letters as `f`, plus `g i` Inbox, `g D` Drafts, `g S` Sent, `g x` Archive, `g g` top. `g p` opens the project picker |
| `f` + key | File the selected message in two keystrokes (see Filing keys) |
| `z` | In a message, show or hide the quoted earlier messages |
| `space` / `T` | `space` opens or closes a conversation in the list. `T` in a message lists the whole conversation across boxes (see Conversations) |
| `c` | Write a new message in the composer, with headers (autocompletion for people and files) and the Markdown body on one screen |
| `ctrl+s` | In the composer, show the preview (`y` sends, `e` or `esc` goes back to writing, `h` to the headers) |
| `ctrl+o` | In the composer, continue in `$MAILDAY_EDITOR`, `$VISUAL`, `$EDITOR` or nvim. Closing it returns to the composer |
| group name in To, Cc, Bcc | In the composer, typing a recipient group's name offers it. `tab` adds one chip such as `◆ Clients (4)` instead of its addresses. Sending, the preview and the send checks use the group's people as they are then, each once (an address typed anywhere in To, Cc or Bcc is not repeated). `backspace` right after a chip removes it, `ctrl+e` turns the last chip into plain addresses to prune, and `ctrl+g` saves To, Cc and Bcc as a group. A chip whose group has been deleted shows in red as `(gone)` and the preview asks you to expand or remove it. A saved draft reopens with plain addresses. Groups live in `groups.txt` in `shared_dir` |
| `esc` | In the composer, close and keep the draft (in the message body `esc esc`, see Vim keys in the composer). The draft appears in the Drafts box beside Sent, where `enter` reopens it to finish and send, `d` deletes it and `u` undoes that |
| `R` / `A` / `F` | Reply (keeps the Cc line), reply to all (adds the other To recipients), forward. All three use the composer, and replies start in the body above the quote |
| `d` | Delete to the account's Trash (Deleted Items on Exchange) |
| `u` | Undo the last move (archive, file, delete, or the reply judge's archive) in the mail pane. It finds the message by its Message-ID when the daemon's server move replaced the file. Gmail's Archive is All Mail, so an archive there is undone in Gmail itself |
| `Z` | Snooze the message (list or reader). It asks when (`tomorrow 9`, `fri`, `volgende week`, `in 3 days`, `tonight`, `in 2 hours`, and a day alone means 08:00) and previews `Back in your inbox Fri 9 Oct 08:00`. See Snooze |
| `!` | The last 30 status messages and errors, with times |
| `y` / `~` / `x` | In a message with a meeting invitation, accept, maybe, decline. The reader shows a card above the message with the time, place, organiser, guests, your answer and clashes with your calendar ("Cancelled" for a cancellation, "Ada accepted" for a reply). The answer goes through the invitation's event in your calendar, so Google or Exchange tells the organiser. Before `mailday-calsync` has fetched that event, Mailday says so and asks for a sync. `J` joins its meeting link, `c` shows it in the calendar's day view, and `a` still archives |
| `S` | In a message, save its attachments to `~/Downloads` (or `$MAILDAY_DOWNLOADS`) |
| `n` / `p` / `N` | In a message, go to the next or previous message of the list, or to the next unread (it says `No more unread` when there is none). In the mail list `n` jumps to the next unread, `pgup` / `pgdn` page and `ctrl+u` / `ctrl+d` go half a page |
| `E` | In a message, open the new-event box filled from it. The title is the subject without `Re:`, `Fwd:`, `Antw:`, `AW:`, `WG:` or `Doorst:`, the time is the first date or time phrase in what the sender wrote (quoted replies and the signature are skipped, dates already past are ignored, and in an old message "tomorrow" counts from the day it was sent), and the sender becomes a `+guest`. Without a date or time in the body the usual defaults apply. Edit it and press `enter` as in Calendar, or `tab` for the full form |
| `O` | In a message, open an attachment from a numbered list (`1`-`9` or `enter` picks, and a single attachment opens at once). It is written to a private folder under the user cache (`~/.cache/mailday/open`, removed after a day) and handed to `xdg-open` (`open` on macOS). Calendar invitations are left out |
| `l` | In a message, open a link from a numbered list, meeting links (Teams, Zoom, Meet) first, each shown as host and path |
| `?` | Show every key |
| `1` through `9` | Select a mail account |
| `1`, `2`, `3` in Calendar | Select Day, Week or Agenda |
| `t` in Calendar | Return to today |
| `g` in Calendar | Jump to a date such as `12 nov`, `next week`, `fri` or `2026-12-01` (a line at the bottom shows the day it reads) |
| `J` in the mail list | Join the meeting in the today strip |
| `/` | Search (see Search and sending checks) |
| `m` | Toggle the selected message's Maildir seen flag |
| `a`, then `a` | Move the selected message into that account's Archive Maildir |
| `s` | Sync every account now through `mailday-syncd` (or `mbsync -a` without it). Rarely needed |
| `o` | Open the mailbox in NeoMutt or the calendar in Evolution |
| `r` | Reload local mail and calendar data (mail reloads by itself) |
| `q` | Quit, or return from a detail screen. Quitting first sends a pending calendar nudge and waits up to 5 seconds for calendar writes and a send in progress (`q` again quits at once) |
| `ctrl+c` | Quit. In the composer it saves the draft first. In the event form or a calendar box it closes that first |

### The reply judge

When `[reply_judge] enabled = true` and the Claude CLI is installed, Mailday reads what you wrote after a reply to an @Reply message and decides whether the original can be archived. It runs `claude -p` with Haiku first and Sonnet when Haiku is unsure. An answer archives the original. A holding reply ("I'll get back to you") keeps it in @Reply. `u` undoes the judge's archive. With the judge off, nothing is archived for you.

## Conversations

Messages that answer each other share one row in the list. The row shows the newest message's sender and date, a count badge (`Budget plan (3)`) and `●` when any of them is unread. A conversation with unread mail sits under New for You, and the list is ordered by each conversation's newest message. `MAILDAY_THREADS=off` (also `0`, `false`, `no`) brings back one row per message.

- **What is one conversation.** Messages are joined through their `Message-ID`, `References` (its first id is the root) and `In-Reply-To`, so a reply whose original is in another box or long gone still joins its siblings. Mail that no id links is joined by its subject, with `Re:`, `Fwd:`, `Fw:`, `Antw:`, `AW:`, `WG:`, `Doorst:` and `[list]` tags removed. That join needs one of the two to be a reply or forward, one sender to be among the other's recipients (whole addresses, so `an@example.com` is not `jan@example.com`) or both to share a sender, and the messages to be less than 60 days apart. Subjects under four characters and generic ones (`hi`, `hello`, `question`, `vraag`, `update`, `meeting`, no subject) never join by subject alone, and two identical newsletters stay apart. Conversations are per account and per box view. A search groups across boxes, with Trash and Spam hits kept last. Drafts are never grouped.
- **`space`** opens the conversation under the cursor, listing its older messages indented under the row (sender and date, since the subject repeats). `space` on any of them closes it again. `enter` on a collapsed row opens the newest unread message, else the newest. `n` and `p` in the reader walk through every message of a conversation.
- **`a a`, `f`, `d`, `m`** on a collapsed row act on every message of it in this box. The status line reports `Archived 3 messages · u undoes`, `Filed 3 messages to Clients`, `Moved 3 messages to Trash` or `Marked 3 messages read` (unread when all were read), and the picker's footer says `Moving 3 messages` while it is open. `Z` snoozes all of it too. After `m` the cursor stays on the conversation (or the single message) wherever it lands. `u` puts all of them back (`Restored 3 messages to Inbox`). On an opened conversation the keys act on the message under the cursor, as everywhere else.
- **`T` in a message** shows its conversation across every box, oldest first, with the box and date of each, the unread ones marked and the message you are reading highlighted. `enter` opens one. It asks notmuch for the thread of the Message-ID (`notmuch search --output=threads id:…`, then `--output=files thread:…`) and shows the loaded messages at once, which is all it has without notmuch. A message notmuch finds outside the loaded mail opens read-only, and the reader says it is no longer in the list when you try to file it.

## Composer and Vim keys

`c`, `R`, `A` and `F` open the composer. Headers (From, To, Cc, Bcc, Subject, Attach) sit above the Markdown body, with autocompletion for people and files.

The message body is edited the Neovim way (`internal/vimedit`). The headers, autocompletion, signatures and preview work the same either way. The body opens in INSERT mode, so typing just works, and the footer shows the mode (`-- INSERT --`, `-- NORMAL --`, `-- VISUAL --`) or the `:` command being typed. `MAILDAY_VIM=off` (also `0`, `false`, `no`) brings back the plain text area, where `esc` closes at once.

- `esc` leaves INSERT. In NORMAL a second `esc` within a second closes the composer and keeps the draft. `ctrl+s` (preview), `ctrl+o` (`$EDITOR`) and `shift+tab` (headers) work in every mode, and `tab` types two spaces in INSERT.
- **Motions**, with counts: `h j k l` and the arrows, `w W b B e E`, `0 ^ $`, `gg G` (`5G` is line 5), `{ }`, `f F t T` with `;` `,`.
- **Edits**: `i a I A o O`, `x X r s S C D J ~`, `dd yy cc >> <<`, operators `d c y > <` with any motion or text object (`iw aw iW aW`, `i" a" i' a'` and backtick quotes, `i( a( i[ a[ i{ a{ i< a<`, `ip ap`), `p P`, `u` and `ctrl+r`, `.` (with a count), and registers `"a`-`"z` and `"_`. All typing in one INSERT session is one undo step.
- **Visual mode**: `v` and `V` select (the selection is highlighted), then `d y c x > < ~ u U` act on it, or `iw`, `i(` and the like extend it.
- **Ex commands**: `:w` saves the draft, `:q` (or `:q!`, `:wq`, `ZQ`) closes keeping the draft, `:x` and `ZZ` open the preview, and `:12` goes to line 12. Drafts are always kept, so there is no way to quit without saving.
- **Search**: `/` and `?` open a prompt in the footer line (shown like `:`). `enter` jumps forward or backward, wrapping round the text with `search wrapped`, and `esc` cancels. The match is a plain substring, case-insensitive unless the pattern has a capital (smartcase). `n` and `N` repeat it in the same or the opposite direction, `*` and `#` search the whole word under the cursor (or the next word on the line), and an empty `/` or `?` reuses the last pattern. A search is a motion, so `d/foo<enter>`, `c?foo<enter>`, `y*` and `dn` work (exclusive, like Vim), a visual selection grows to the match, and `.` repeats them. Every match of the last pattern stays highlighted in a yellow of its own until `:noh` (or `:nohlsearch`). `esc` does not clear it, and the next search brings it back. While you type the pattern the first match from the cursor is highlighted and scrolled into view, and `esc` puts the cursor and the scroll back. There are no regular expressions or offsets (`/foo/e`).
- **Wrapped lines**: `gj` and `gk` move by screen line, keeping the display column (wide East Asian characters count two cells), with counts (`3gj`) and as operator targets. `g0` and `g$` go to the start and end of the screen line. `j` and `k` still move by line.
- **Macros**: `q` and a letter `a`-`z` records, `q` ends it (the footer says `recording @a`), `@a` plays it, `@@` plays the last one, and `3@a` plays it three times. A played key goes through the same handling as a typed one, so `u` undoes the macro one change at a time and `.` repeats the last change in it. A failed motion (`j` on the last line, a search that finds nothing) stops the macro and its remaining counts, which is how a recursive macro (`@a` inside `a`) ends. Recursion stops at 100 levels.
- **Not there**: marks, `%`, `R` replace mode, `p` over a selection, `ctrl+a`/`ctrl+x`, `ctrl+d/u` and page keys, `gu gU g~`, Ex ranges and `:s`, block-wise visual, and `.` after a visual operator repeats the keys from the cursor rather than the same-sized region.

## Snooze

`Z` files a message in the account's `@Snoozed` box and notes when it should return. The `@Snoozed` box lists each message by its due time (`Fri 08:00`), soonest first. The status line says `Snoozed until Fri 08:00 · u undoes`, and `u` moves the message back and drops the entry.

- `Z` on a collapsed conversation row snoozes every message of it in this box, one entry each with the same due time (`Snoozed 4 messages until Fri 08:00 · u undoes`, and the prompt says `Snooze · Budget (4 messages)`). Messages without a Message-ID stay and the status says so. `u` brings all of them back.
- On an opened conversation row or in the reader, `Z` snoozes the one message.
- `Z` on a message already in `@Snoozed` only changes the time.
- `@Snoozed` is not offered by `f`, because a message put there by hand has no entry to bring it back.

**The list.** Entries live in `snooze.json` in `shared_dir` (`MAILDAY_SNOOZE_FILE` overrides the path), which a synced folder can share between machines. Each entry holds the Message-ID, account, due time, the box it came from and the subject, keyed by account and Message-ID. Writers take a lock file beside it, read, change and rename a temporary file over it, so Mailday and the daemon on each machine never lose each other's entries. Every write leaves `snooze.json.bak`. A file that does not parse is read from that backup when the backup parses (Mailday says `snooze list was damaged; restored from the backup` once, and the daemon logs it), and the next write keeps the damaged file as `snooze.json.corrupt-<unix time>` before replacing it. Without a usable backup the problem is reported and the file is never overwritten.

**The folder.** Mailday creates the `@Snoozed` Maildir (with `cur`, `new`, `tmp`) when the account already has other `@` folders. The server folder has to come from your mbsync config, for example a channel that matches `@*`. An account without `@` folders is refused, since nothing would sync it.

**Coming back.** `mailday-syncd` reads the list every minute, at start and after a resume. For each due entry it makes one IMAP connection per account, finds the message in `@Snoozed` by its Message-ID (an exact match, as for filing), clears `\Seen` and MOVEs it to INBOX, then syncs both boxes. Only a real MOVE is used. The entry goes when the message has moved or is no longer in `@Snoozed`. It stays while the server is unreachable, and for 15 minutes when the message is not in `@Snoozed` yet (mbsync may not have uploaded it). Entries of accounts a machine does not sync are left for the one that does.

**In the calendar.** Snoozed mail does not disappear. The agenda (3) and the day view's panel (under the grid on a narrow terminal) list each message from `@Snoozed` at its due time as a dim row of its own, such as `08:00  ✉ back: Re: Grant application · Ben Example`, between the events by time. A message already due shows under today as `✉ due now`. Arrow keys move over these rows like events. `enter` opens the message in the reader (`esc` returns to the row), `Z` snoozes it again, and the event keys (`m`, `e`, `d`, `a`, `~`, `x`) say `That is a snoozed message, not an event`. The week grid has no such rows. The rows are read from the snooze list when mail loads. On the mail list, the today strip says `✉ back at 14:00: subject` when nothing else is shown and a snoozed message returns within the hour.

**Two machines.** Both run the same pass. The second finds the message gone from `@Snoozed` and in INBOX, and only drops the entry. The move is by UID, so there is no duplicate, and a lost race is not logged as an error. A problem (offline, no MOVE) is logged once, not every minute.

## Calendar

`tab` switches to Calendar. `1`, `2` and `3` pick Day, Week or Agenda, left and right move between spans, `t` returns to today, and `g` jumps to a date.

The week view is a Monday-first time grid. Events are blocks as long as they last, overlapping events sit side by side, all-day events run along the top, and a line marks the current time. The day view adds a panel with the selected event in full (length, how soon, calendar, place, call link, notes). The agenda fades what is over, marks Today and Tomorrow, shows a `now` line with the wait until the next event, and scrolls with the selection.

The mail list header gets a **today strip** when a timed event is on now or starts within the hour, such as `next 14:00 Seminar in 25 min · Atrium · J joins` or `now Board meeting until 15:00 · J joins`. `J` on the mail list opens its meeting link. The Calendar tab shows what is left today (`Calendar · 3`). Both read the calendar range on screen, so they are empty while you browse another week.

## Calendar editing

Events are added, rebooked, renamed and deleted from the Calendar pane. The change shows at once (in italics, with …) and `mailday-calendar` sends it to Google or Exchange in the background. When the server refuses, the event goes back and the status line says why.

| Key | Action |
|---|---|
| `n` | New event from one line, in a box over the calendar. Each word is coloured by what it was read as (date, time, length, repeat, `@`place, `#`calendar, `+`people, unknown names in red), the fields below fill in with the same colours, and fields you did not give show the default in grey. It warns when the time overlaps something. `tab` opens the full form with everything filled in, and `shift+tab` cycles the calendar. Examples are `Lunch with Ada tomorrow 12:30 1h @Atrium #Work`, `Gym fri 18:30-20:00` and `Conference 12 nov all day #Home`. Without a date the event lands on the selected day, without a time at 9:00 (or the next half hour today), and without a length it lasts an hour |
| `m` | Rebook the selected event, in a box that shows the time now and after (and any overlap). It takes `fri` (same time), `14:00` (same day), `fri 10:00`, `+1d`, `-30m`, `+1w`, `14:00-15:30`, `2h` (new length) and `next free` (first gap as long as the event, weekdays 08:00–18:00) |
| `<` `>` / `+` `-` | Move the selected event a day / half an hour. Presses add up and one update is sent when you stop |
| `e` | Edit every field in a form: title, when (`2026-10-08 18:30-20:00`, or any phrase `m` takes), calendar (`←` `→`, each marked Google or Exchange, and choosing another moves the event there), place, people to invite, and notes (`ctrl+o` opens them in `$EDITOR`). `ctrl+s` saves |
| `N` | The same form for a new event |
| `+name` | In the `n` line, invites the best match in the address book (or type an address). With `next free` (in `n` or `m`), Exchange is asked when they are busy and the event goes in the first time you are all free, as in `Plan workshop 1h next free +ben #Work` |
| `every …` | Repeats a new event with `every mon`, `every mon and wed`, `every weekday`, `daily`, `every 2 weeks fri` or `monthly`, with `until 20 dec` or `10 times`. Changes apply to one occurrence, and `D D` deletes the whole series. Repeating events show ↻ |
| `a` `~` `x` | Answer an invitation: accept, maybe, decline. The organizer is told |
| `d` `d` | Delete |
| `u` | Undo the last calendar change made in this session, for 10 minutes (the calendar pane only, and `u` in mail still undoes a move). A new event is deleted again. A rebook, nudge, rename or edit goes back to the fields that change touched, among title, place, times and notes (what a sync changed since stays, and it works when another week is on screen). A deleted event is created again with its title, time, place and notes, as a new event whose guests are not re-invited (a repeating series comes back as a single event). An answer goes back to the one before. A move to another calendar is refused, and so is an answer when you had given none. Invitations already sent stay sent. Only one step is kept |
| `J` | Join the meeting link (Teams, Zoom, Meet) |

**Dates** understand today, tomorrow, weekday names (the next one after today), Dutch day words, `in 3 days`, `in 2 weeks`, `in a month`, `next week` (its Monday), `fri next week`, `this weekend`, `the 12th`, `12 oct`, `oct 12`, `12/10` (day first) and `2026-10-12`. **Times** can be `14:00`, `14.30`, `9am`, `2p`, `2.30pm`, `14u`, `14u30`, `noon`, `at 9`, and parts of the day after a day (`tomorrow afternoon`, `this evening`, `tonight`). A bare 1 to 6 is the afternoon (`at 3` is 15:00 and `2-4` is 14:00–16:00, and `03:00` writes the night). **Lengths** are `1h`, `90m`, `1h30`, `for 2h`, `90 minutes`, `an hour` and `half an hour`.

**Dutch** works as well, in lower case. It reads `volgende vrijdag`, `komende week`, `dit weekend`, `over 3 dagen`, `over twee weken`, `over een maand`, the short days `ma di wo do vr za zo` (`do`, `za` and `zo` only beside a date or time, so `Things to do` stays a title), `mrt`, `half twee` (13:30, as in Dutch, the half hour before two), `kwart over drie` (15:15), `kwart voor vier` (15:45), `om 3 uur` (15:00), `om 14.00 uur`, `morgen 3 uur` (a number of hours straight after a day is a time, and elsewhere `3 uur` is a length), `de hele dag` and `eind van de middag` (17:00).

**Typing is forgiving**, and the box says what it did on a `read as` line. It accepts shorthand (`tom`, `tmrw`, `2moro` for tomorrow, `tdy` for today, `nxt`, `wknd`), unfinished words (`tomo`, `wedn`, `septem`) and typos in longer words (`tommorow`, `wensday`, `firday`). Only words typed in lower case are reinterpreted, so a name in a title stays a name (`Coffee with Tom tom 3` is coffee with Tom tomorrow at 15:00), and short words are never corrected (`Tennis match` stays a match, not March). Quotes keep anything literal, as in `Call "tom"`. Plain numbers stay in the title (`Room 12`).

**Invitations** (Google events organised by someone else, Exchange meetings where you are not the organiser) cannot be edited, only answered. They show `?` until answered and `~` for maybe, and are struck through when declined. The agenda shows who sent them.

**Moving between calendars.** Mailday uses Google's own move between Google calendars. Otherwise it creates the event in the target and deletes the original, carrying title, time, place and notes. Exchange events carry their real notes (the EWS body, fetched with GetItem), so Teams meetings have a join link for `J`.

**What needs which login.** `mailday-calendar` writes Google calendars through the module named by `[calendar] google_script` (a `gcal.py`-compatible script, using a service account with writer access to each calendar) and the Exchange calendar through EWS with the `mailday-exchange` token. Google lets a service account create, move and delete events but not invite people or answer invitations. Those act as you with the script's OAuth token. Run `mailday-calendar login` once on each machine (it opens the browser). While the Google Cloud OAuth app is in Testing mode that login lasts 7 days, and setting its publishing status to In production (unverified is fine for one user) keeps it. Without `google_script`, Google events can be viewed but not edited, and Google invitations cannot be answered. Exchange editing and Exchange invitation answers need only the token. Moving a meeting you organised sends the update to its attendees, as Outlook and Google do. `mailday-calsync` and `mailday-exchange` put each event's id (`X-MAILDAY-ID`) and whether it is yours (`X-MAILDAY-EDITABLE`) in the ICS files. `google_own` lists your own Google addresses, so events you organise count as yours to move.

## Calendar sources

`mailday-calsync` (installed by `make install`) writes one ICS file per calendar to `~/.local/share/mailday/calendars` (`MAILDAY_ICS_DIR` overrides), covering 30 days back and 180 ahead (`--back`, `--ahead`). The daemon runs it on the schedule described under Live sync. Files are replaced atomically and a calendar that failed to fetch keeps its last good copy. The other calendars are still written, each failure is printed to stderr, and the run exits with status 2 so the daemon reports it. It handles three sources.

- **iCal feeds.** Every entry under `[calendar] feeds` is downloaded as published and saved as `<name>.ics`.
- **Google calendars.** When `[calendar] google_script` points at a `gcal.py`-compatible module, `mailday-calsync` runs its `fetch` command, which reads Google calendars through a service account and needs no browser login. A missing script path prints a note and skips Google. `GCAL_SCRIPT` overrides the config.
- **Exchange.** When `[exchange] user` is set and a token exists, `mailday-calsync` also runs `mailday-exchange sync`.

When Evolution holds the same event, the ICS copy wins.

**Microsoft 365 calendar.** Set `[exchange] user` to your mailbox address and run `mailday-exchange authorize` once. It opens a browser login with Thunderbird's registered Microsoft client ID, the one Thunderbird uses for Exchange accounts, and stores the token gpg-encrypted to your own key at `~/.config/mailday/ews-token.gpg` (`[exchange] token` or `MAILDAY_EXCHANGE_TOKEN` change the path). Some tenants do not consent to that client ID, and `[exchange] client_id` then takes the ID of an app you registered. The login has to happen on the machine whose browser opens, because Microsoft redirects to `localhost`. The token file works on any machine holding the same gpg key. After that, `mailday-exchange sync` reads the calendar folder through EWS `FindItem` and writes `<calendar>.ics`, where the name comes from `[exchange] calendar` (default `Outlook`). `ews_url` defaults to Microsoft 365's endpoint, and `mailbox_tz` sets the zone for the mailbox when `home_zone` is not what the mailbox uses.

**Evolution.** Evolution Data Server places selected remote calendars under its cache directory. Mailday discovers those source files, reads each `cache.db` with `sqlite3 -readonly`, expands recurring events, and combines them with local ICS files. A Microsoft 365 or Google calendar appears after Evolution has created and synced its calendar source. Exchange mail does not depend on Evolution, because mbsync already writes it into Maildir.

**Plain ICS files.** Any `.ics` file placed in `~/.local/share/mailday/calendars` is read.

## Travelling

- **macOS** switches time zone by itself.
- **Linux** only changes zone when `tzupdate` runs. `make install-tz` prints the one `sudo install` command that puts a NetworkManager dispatcher script in place, so `tzupdate` runs (in the background, at most 20 seconds, logged as `mailday-tzupdate` in the journal) whenever a network comes up.
- **`mailday-syncd`** looks at the system zone every minute and after a resume. When it changes, the daemon switches to it, logs `time zone is now Asia/Tokyo`, refreshes the calendar, and meeting reminders follow the new zone. `mailday-syncd status` shows the zone.
- **Mailday** checks the system zone on its 15-second clock tick. When it changes, it switches too, reloads the calendar and says `Time zone is now Asia/Tokyo`, with no restart.

The home zone is `MAILDAY_HOME_TZ`, else `[calendar].home_zone`, else the zone Mailday started in. While the machine is in another zone than home, the day panel, the event screen and the agenda show the home time beside the local one (`16:00–17:00 · 09:00–10:00 Berlin`), and the footer rule says `Tokyo time`. The week grid stays local.

## Read receipts

Mailday does not ask for read receipts by default. Receipts that arrive (from Outlook, Thunderbird and anything else that sends a `multipart/report` with `report-type=disposition-notification`) are shown.

- **Who read it.** Receipts are matched to your message by its Message-ID. In Sent the row says `✓ read` (`deleted unread` when the recipient deleted it unread). The message itself lists each answer, as in `Read by Ben Example · Tue 7 Oct 14:02`.
- **Out of the way.** The receipt messages are left out of every box in the list, so the inbox does not fill with `Read: …`. The footer says `2 read receipts hidden` for the box on screen. Nothing is moved or deleted, and a `/` search through notmuch still finds them.
- **Asking for them.** `MAILDAY_READ_RECEIPTS=on` makes new drafts carry `Disposition-Notification-To:` with your From address. There is no key for it. A draft's `read-receipt: yes` line, which is never sent, does the same for that draft.

## Safety

Mail headers, bodies, calendar names, event text and command errors are stripped of terminal control sequences. Message previews stop at 12 MiB. Archive and unread changes only rename Maildir files, and a later mbsync run sends those changes to the server.

## Environment variables

| Variable | Effect |
|---|---|
| `MAILDAY_CONFIG` | Path of `config.toml` |
| `MAILDAY_MAIL_ROOT` | Maildir roots, separated like `PATH` (default `~/Mail` and `~/.local/share/mail`) |
| `MAILDAY_SNOOZE_FILE` | Path of the snooze list (default `snooze.json` in `shared_dir`) |
| `MAILDAY_GROUPS_FILE` | Path of the recipient groups (default `groups.txt` in `shared_dir`) |
| `MAILDAY_LEASE_DIR` | Directory of job leases (default `leases/` in `shared_dir`) |
| `MAILDAY_HOME_TZ` | Home time zone, ahead of `[calendar].home_zone` |
| `MAILDAY_SYNCD_SOCKET` | Socket path of `mailday-syncd` |
| `MAILDAY_SEND_DELAY` | Seconds `y` waits before sending (default 10, 0 sends at once) |
| `MAILDAY_AUTO_ADVANCE` | `off` returns to the list after `a a`, `f`, `d` or `m` in a message |
| `MAILDAY_THREADS` | `off` (also `0`, `false`, `no`) shows one row per message |
| `MAILDAY_VIM` | `off` (also `0`, `false`, `no`) uses the plain text area in the composer |
| `MAILDAY_EDITOR` | Editor for `ctrl+o`, ahead of `$VISUAL` and `$EDITOR` |
| `MAILDAY_DOWNLOADS` | Folder for `S` (default `~/Downloads`) |
| `MAILDAY_FILE_SEARCH` | `walk` forces the plain directory walk in the attach field |
| `MAILDAY_READ_RECEIPTS` | `on` asks for read receipts on new drafts |
| `MAILDAY_CALENDAR` | Calendar the new-event box starts on (default the last one used, else the first) |
| `MAILDAY_THEME` | Path of a theme file |
| `MAILDAY_ICS_DIR` | Directory of ICS files written by `mailday-calsync` |
| `MAILDAY_EXCHANGE_TOKEN` | Path of the Exchange token, ahead of `[exchange].token` |
| `MAILDAY_EXCHANGE_USER` | Exchange mailbox address, ahead of `[exchange].user` |
| `MAILDAY_EXCHANGE_MAILBOX_TZ` | Mailbox time zone, ahead of `[exchange].mailbox_tz` |
| `MAILDAY_GOOGLE_OWN` | Your Google addresses, comma-separated, ahead of `[calendar].google_own` |
| `GCAL_SCRIPT` | Path of the Google script, ahead of `[calendar].google_script` |
