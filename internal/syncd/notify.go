package syncd

import (
	"context"
	"fmt"
	"log"
	"mime"
	"net/mail"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

var wordDecoder = mime.WordDecoder{}

// notifyNewMail shows one desktop notification per sync that brought new
// inbox mail: sender and subject of the first few messages.
func (d *Daemon) notifyNewMail(account string, paths []string) {
	if d.verbose {
		log.Printf("%s: %d new message(s) in the inbox", account, len(paths))
	}
	if !d.options.Notify {
		return
	}
	// Read receipts are not mail to read: Mailday files them under the
	// message they answer.
	paths = slices.DeleteFunc(slices.Clone(paths), isReadReceipt)
	if len(paths) == 0 {
		return
	}
	// With the MacBook and the desktop both awake, only the one in use
	// speaks up; a locked or idle machine stays quiet.
	if !userActive() {
		if d.verbose {
			log.Printf("%s: not notifying, nobody at this machine", account)
		}
		return
	}
	var lines []string
	for index, path := range paths {
		if index == 3 {
			lines = append(lines, fmt.Sprintf("and %d more", len(paths)-3))
			break
		}
		from, subject := summary(path)
		lines = append(lines, fmt.Sprintf("%s: %s", from, subject))
	}
	title := fmt.Sprintf("New mail in %s", account)
	if len(paths) > 1 {
		title = fmt.Sprintf("%d new in %s", len(paths), account)
	}
	// On Linux the notification has an Open button: Mailday on the message
	// (or the inbox, for several).
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("notify-send"); err == nil {
			go func() {
				choice := notifyWithActions(title, strings.Join(lines, "\n"), "mail-unread", []string{"open=Open"})
				if choice == "open" || choice == "default" {
					if len(paths) == 1 {
						openMailday("--open", paths[0])
					} else {
						openMailday()
					}
				}
			}()
			return
		}
	}
	command := notifyCommand(title, strings.Join(lines, "\n"))
	if command == nil {
		return
	}
	if err := command.Start(); err == nil {
		go command.Wait()
	}
}

// notifyCommand picks notify-send on Linux, terminal-notifier or osascript
// on macOS.
func notifyCommand(title, body string) *exec.Cmd {
	if path, err := exec.LookPath("notify-send"); err == nil {
		return exec.Command(path, "-a", "Mailday", "-i", "mail-unread", title, body)
	}
	if path, err := exec.LookPath("terminal-notifier"); err == nil {
		return exec.Command(path, "-title", title, "-message", body, "-group", "mailday", "-sound", "default")
	}
	if path, err := exec.LookPath("osascript"); err == nil {
		quote := func(value string) string {
			return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
		}
		return exec.Command(path, "-e", "display notification "+quote(body)+" with title "+quote(title))
	}
	return nil
}

// idleLimit is how long without keyboard or mouse counts as away.
const idleLimit = 5 * time.Minute

// activityTimeout bounds each probe of userActive: a stuck logind or
// IOKit must read as "can't tell", not block the caller.
const activityTimeout = 3 * time.Second

// userActive reports whether someone is using this machine: on Linux the
// session is not locked (hyprlock, or logind's LockedHint/IdleHint), on macOS
// the keyboard or mouse was used within idleLimit.
func userActive() bool {
	ctx, cancel := context.WithTimeout(context.Background(), activityTimeout)
	defer cancel()
	return userActiveWithin(ctx)
}

// probeCommand is a newCommand that is forgotten a second after its context
// ends, so the 3 s limit of userActive is a limit in practice too.
func probeCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	command := newCommand(ctx, name, args...)
	command.WaitDelay = time.Second
	return command
}

func userActiveWithin(ctx context.Context) bool {
	switch runtime.GOOS {
	case "darwin":
		output, err := probeCommand(ctx, "ioreg", "-c", "IOHIDSystem", "-d", "4").Output()
		if err != nil {
			return true
		}
		for _, line := range strings.Split(string(output), "\n") {
			if !strings.Contains(line, `"HIDIdleTime"`) {
				continue
			}
			fields := strings.Fields(line)
			nanoseconds, err := strconv.ParseInt(fields[len(fields)-1], 10, 64)
			if err != nil {
				return true
			}
			return time.Duration(nanoseconds) < idleLimit
		}
		return true
	case "linux":
		if probeCommand(ctx, "pidof", "hyprlock").Run() == nil {
			return false
		}
		output, err := probeCommand(ctx, "loginctl", "show-session", "auto", "-p", "LockedHint", "-p", "IdleHint").Output()
		if err == nil && strings.Contains(string(output), "=yes") {
			return false
		}
		return true
	default:
		return true
	}
}

func summary(path string) (from, subject string) {
	file, err := os.Open(path)
	if err != nil {
		return "?", ""
	}
	defer file.Close()
	message, err := mail.ReadMessage(file)
	if err != nil {
		return "?", ""
	}
	from = decode(message.Header.Get("From"))
	if address, err := mail.ParseAddress(message.Header.Get("From")); err == nil {
		from = address.Name
		if from == "" {
			from = address.Address
		}
	}
	subject = decode(message.Header.Get("Subject"))
	if subject == "" {
		subject = "(no subject)"
	}
	return clip(from, 40), clip(subject, 80)
}

func decode(value string) string {
	if decoded, err := wordDecoder.DecodeHeader(value); err == nil {
		return decoded
	}
	return value
}

func clip(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return value
}

// isReadReceipt says the file is an RFC 8098 read receipt (multipart/report;
// report-type=disposition-notification).
func isReadReceipt(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	message, err := mail.ReadMessage(file)
	if err != nil {
		return false
	}
	kind, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	return err == nil && kind == "multipart/report" && strings.EqualFold(params["report-type"], "disposition-notification")
}
