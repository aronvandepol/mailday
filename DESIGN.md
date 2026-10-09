---
name: Mailday
description: Local mail and calendar TUI based on the HEY TUI
---

# Design System

## 1. Overview

**Creative North Star "The HEY TUI on local data"**

The visual source is `basecamp/hey-cli/internal/tui` and the nine screenshots published at `https://www.hey.com/omarchy/`. Mail and Calendar occupy the full terminal. Thin rules, centered navigation, dense rows, and a persistent key guide provide the structure.

The interface rejects split dashboard cards and rounded containers. Each screen should remain recognizably close to the corresponding HEY screenshot while local Maildir and Evolution data replace HEY API records.

**Key Characteristics**

- Full-width sections
- Two-line mail rows
- Seven-column week grid
- ANSI theme colors
- Visible keyboard guidance

## 2. Colors

The terminal and active Omarchy theme own the palette.

- **Chrome** uses ANSI blue for rules, labels, and key names.
- **Primary** uses ANSI bright blue for selection and active content.
- **Active navigation** uses ANSI yellow.
- **Sender and links** use ANSI bright cyan.
- **Unread state** uses ANSI red.
- **Secondary text** uses the terminal foreground with the faint attribute.
- **Calendar events** use ANSI blue, magenta, cyan, green, yellow, and red backgrounds selected by calendar source.

**The Terminal Palette Rule.** UI chrome must use ANSI slots. Fixed RGB values cannot replace the user's current Omarchy colors.

## 3. Typography

The terminal controls the typeface. The interface uses the user's monospace font at its configured size.

- **Navigation** uses bold text and underlined shortcut letters.
- **Subjects** use bold emphasized text.
- **Senders** use bold link color.
- **Dates and excerpts** use faint text.
- **Message bodies** use normal terminal text with a readable line length.

**The Density Rule.** Mail rows use two lines. The first carries subject and date. The second carries sender and excerpt.

## 4. Elevation

The TUI has no shadows and no floating surfaces. Rules, spacing, foreground intensity, and event fills provide hierarchy.

**The Flat Screen Rule.** Full-width content stays on the terminal background. Rounded panels and simulated cards are prohibited.

## 5. Components

### Navigation

A centered section row sits below a top rule carrying the application name and active account. A second labeled rule and row show the current Mail or Calendar subsection.

### Mail list

Unread and previously seen messages form labeled sections. Each selected message uses a left cursor bar. Unread messages also carry a dot.

### Weekly calendar

Seven day columns sit on open terminal space with dotted vertical dividers. Timed events use a faint time label and a filled event bar. All-day events sit in a separate band at the foot of the grid.

### Message view

The message subject is centered below navigation. Sender and date precede the body. A full-width rule separates messages or major body sections.

### Key guide

A full-width rule separates content from a wrapping list of key and action pairs.

## 6. Do's and Don'ts

### Do

- **Do** follow the rendering shape in the official HEY TUI source.
- **Do** keep shortcut keys visible and styled consistently.
- **Do** preserve full-width open space in Mail and Calendar.
- **Do** retain non-color selection markers.
- **Do** test at wide and narrow terminal sizes.

### Don't

- **Don't** restore split dashboard cards.
- **Don't** use rounded panel borders.
- **Don't** hardcode a Tokyo Night, Retro 82, or Rose Pine palette.
- **Don't** hide the agenda behind a separate command.
- **Don't** copy HEY service behavior that local Maildir and Evolution cannot support.
