package tui

import (
	"os"

	tea "charm.land/bubbletea/v2"
)

func Run(mailStore MailStore, calendarStore CalendarStore, options Options) error {
	options.Live = true
	program := tea.NewProgram(NewModel(withPush(mailStore), calendarStore, options))
	stop := captureStderr()
	_, err := program.Run()
	afterRun(err, stop(), os.Stdin)
	return err
}
