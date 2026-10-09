package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/maildir"
)

func TestListDates(t *testing.T) {
	// Thursday 15 October 2026, 14:30.
	now := time.Date(2026, 10, 15, 14, 30, 0, 0, time.Local)
	for _, test := range []struct {
		name string
		at   time.Time
		want string
	}{
		{"this morning", time.Date(2026, 10, 15, 8, 5, 0, 0, time.Local), "08:05"},
		{"just after midnight", time.Date(2026, 10, 15, 0, 1, 0, 0, time.Local), "00:01"},
		{"yesterday", time.Date(2026, 10, 14, 23, 59, 0, 0, time.Local), "Wed"},
		{"monday of this week", time.Date(2026, 10, 12, 9, 0, 0, 0, time.Local), "Mon"},
		{"sunday before this week", time.Date(2026, 10, 11, 23, 0, 0, 0, time.Local), "11 Oct"},
		{"earlier this year", time.Date(2026, 1, 2, 9, 0, 0, 0, time.Local), "2 Jan"},
		{"last year", time.Date(2025, 12, 31, 9, 0, 0, 0, time.Local), "31 Dec 2025"},
		{"later today (clock skew)", time.Date(2026, 10, 15, 23, 0, 0, 0, time.Local), "23:00"},
		{"tomorrow (clock skew)", time.Date(2026, 10, 16, 9, 0, 0, 0, time.Local), "16 Oct"},
		{"none", time.Time{}, ""},
	} {
		if got := formatListDate(test.at, now); got != test.want {
			t.Errorf("%s: %q, want %q", test.name, got, test.want)
		}
	}
	if width := displayWidth("30 Sep 2026"); width > mailDateWidth {
		t.Errorf("the longest date is %d wide, the column %d", width, mailDateWidth)
	}
}

func TestListDatesFollowTheMachineZone(t *testing.T) {
	seoul, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Skip("no tz database")
	}
	previous := time.Local
	time.Local = seoul
	defer func() { time.Local = previous }()
	// 23:30 UTC on the 14th is 08:30 on the 15th in Seoul.
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, seoul)
	if got := formatListDate(time.Date(2026, 10, 14, 23, 30, 0, 0, time.UTC), now); got != "08:30" {
		t.Fatalf("got %q", got)
	}
}

func TestMessageAge(t *testing.T) {
	now := time.Date(2026, 10, 15, 14, 30, 0, 0, time.Local)
	for _, test := range []struct {
		ago  time.Duration
		want string
	}{
		{20 * time.Minute, "20m"}, {5 * time.Hour, "5h"}, {23*time.Hour + 59*time.Minute, "23h"},
		{24 * time.Hour, "1d"}, {9*24*time.Hour + time.Hour, "9d"}, {120 * 24 * time.Hour, "120d"}, {-time.Hour, "0m"},
	} {
		if got := messageAge(now.Add(-test.ago), now); got != test.want {
			t.Errorf("%v ago: %q, want %q", test.ago, got, test.want)
		}
	}
}

func TestReplyAndWaitingShowAgeInTheList(t *testing.T) {
	now := time.Date(2026, 10, 15, 14, 30, 0, 0, time.Local)
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.now = func() time.Time { return now }
	model.width, model.height = 100, 30
	model.loadingMail = false
	model.accounts = []string{"a"}
	model.boxes = []string{maildir.InboxBox, "@Reply", "@Waiting"}
	model.messages = []maildir.Message{
		{Path: "/m/a/Inbox/cur/1:2,S", Account: "a", Box: maildir.InboxBox, Subject: "In the inbox", Date: now.Add(-9 * 24 * time.Hour)},
		{Path: "/m/a/@Reply/cur/2:2,S", Account: "a", Box: "@Reply", Subject: "Needs an answer", Date: now.Add(-9 * 24 * time.Hour)},
		{Path: "/m/a/@Waiting/cur/3:2,S", Account: "a", Box: "@Waiting", Subject: "Awaiting them", Date: now.Add(-3 * time.Hour)},
	}
	rowFor := func(subject string) string {
		for _, line := range strings.Split(ansi.Strip(model.View().Content), "\n") {
			if strings.Contains(line, subject) {
				return line
			}
		}
		t.Fatalf("no row for %q", subject)
		return ""
	}
	if row := rowFor("In the inbox"); !strings.HasSuffix(strings.TrimSpace(row), "6 Oct") {
		t.Errorf("inbox row: %q", row)
	}
	model.mailBox = 1
	if row := rowFor("Needs an answer"); !strings.HasSuffix(strings.TrimSpace(row), "9d") {
		t.Errorf("@Reply row: %q", row)
	}
	model.mailBox = 2
	if row := rowFor("Awaiting them"); !strings.HasSuffix(strings.TrimSpace(row), "3h") {
		t.Errorf("@Waiting row: %q", row)
	}
}

func TestBoxLabelsCountUnreadOnly(t *testing.T) {
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.accounts = []string{"a", "b"}
	model.boxes = []string{maildir.InboxBox, "@Reply", "@Waiting", draftsBox, maildir.SentBox, maildir.ArchiveBox}
	model.messages = []maildir.Message{
		{Account: "a", Box: maildir.InboxBox, Unread: true}, {Account: "a", Box: maildir.InboxBox, Unread: true},
		{Account: "b", Box: maildir.InboxBox, Unread: true}, {Account: "a", Box: maildir.InboxBox},
		{Account: "a", Box: "@Reply"}, {Account: "a", Box: "@Reply"},
		{Account: "a", Box: maildir.SentBox, Unread: true}, {Account: "a", Box: maildir.ArchiveBox, Unread: true},
		{Account: "a", Box: draftsBox},
	}
	type shown struct {
		label string
		dim   bool
	}
	read := func() []shown {
		var out []shown
		items, _ := model.narrowBoxItems()
		for _, item := range items {
			out = append(out, shown{item.label, item.dim})
		}
		return out
	}
	want := []shown{{"Inbox 3", false}, {"Reply", true}, {"Waiting", true}, {"Drafts", false}, {"Sent", false}, {"Archive", false}}
	if got := read(); !equalShown(got, want) {
		t.Fatalf("all accounts: %v, want %v", got, want)
	}
	model.mailAccount = 0 // account a
	want[0] = shown{"Inbox 2", false}
	if got := read(); !equalShown(got, want) {
		t.Fatalf("account a: %v, want %v", got, want)
	}
	model.mailAccount = 1
	model.messages[2].Unread = false
	want[0] = shown{"Inbox", true}
	if got := read(); !equalShown(got, want) {
		t.Fatalf("account b, all read: %v, want %v", got, want)
	}
}

func equalShown[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
