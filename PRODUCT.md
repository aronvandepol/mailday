# Product

## Surface Type

product

## Users

A terminal user managing several personal and university mail accounts alongside Google and Microsoft 365 calendars on Omarchy.

## Product Purpose

`mailday` puts the working day in one terminal application. Mail remains synchronized by mbsync, sending remains available through NeoMutt, and calendar data remains owned by Evolution Data Server. Success means the user can scan mail, read messages, and inspect the week without moving between applications.

## Brand Personality

Opinionated, compact, calm.

## Anti-references

- Split dashboard cards
- Rounded panel borders
- A web dashboard compressed into a terminal
- Decorative color that ignores the active terminal theme
- Sparse mail rows that hide useful context

## Design Principles

- Follow the HEY TUI interaction and rendering patterns.
- Keep Mail and Calendar as full-screen sections.
- Put navigation and keyboard help on screen.
- Let the active Omarchy palette determine color.
- Keep remote services behind local Maildir and Evolution adapters.

## Accessibility and Inclusion

Every action must work from the keyboard. Selection must use a marker as well as color. Text from mail and calendars must be stripped of terminal control sequences. Narrow terminals must retain every action through a single-column view. `NO_COLOR` must remain usable.
