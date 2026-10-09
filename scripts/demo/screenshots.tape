# Screenshots for the README, taken from the demo home.
#   make demo && vhs scripts/demo/screenshots.tape
# Run from the repository root. Nothing here touches your own mail.

Output demo-home/screenshots.gif
Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 15
Set Width 1600
Set Height 940
Set Padding 18
Set Theme "TokyoNight"
Set TypingSpeed 40ms

Hide
Type "source demo-home/env.sh && clear && exec bin/mailday" Enter
Sleep 3s
Show
Sleep 500ms
Screenshot docs/screenshots/mail.png
Sleep 1s

# The reader: the client thread, found by search.
Hide
Type "/"
Sleep 300ms
Type "packaging round 2"
Sleep 1.5s
Enter
Sleep 500ms
Enter
Sleep 1s
Show
Screenshot docs/screenshots/reader.png
Sleep 1s

# The composer: a reply to that message.
Hide
Type "R"
Sleep 1s
Type "Thanks Marta, glad pine won. I'll move the leaf mark down 2mm and put legal's line on the back panel, then send the **final dielines** on Friday."
Sleep 500ms
Show
Screenshot docs/screenshots/compose.png
Sleep 1s

# The calendar week, then the agenda.
Hide
Escape
Sleep 300ms
Type ":q"
Enter
Sleep 500ms
Escape
Sleep 300ms
Escape
Sleep 300ms
Tab
Sleep 1s
Type "2"
Sleep 800ms
Show
Screenshot docs/screenshots/calendar.png
Sleep 1s
Hide
Type "3"
Sleep 800ms
Show
Screenshot docs/screenshots/agenda.png
Sleep 1s

Hide
Type "q"
