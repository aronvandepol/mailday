package mbsyncrc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `IMAPAccount gmail
Host imap.gmail.com
User me@gmail.com
PassCmd "gpg --quiet -d ~/x.gpg"
TLSType IMAPS

IMAPStore gmail-remote
Account gmail

MaildirStore gmail-local
SubFolders Verbatim
Path /m/gmail/
Inbox /m/gmail/Inbox

Channel gmail-inbox
Far :gmail-remote:
Near :gmail-local:
Patterns "INBOX"

Channel gmail-sent
Far :gmail-remote:"[Gmail]/Sent Mail"
Near :gmail-local:Sent

Channel gmail-tags
Far :gmail-remote:
Near :gmail-local:
Patterns "@*"

Group gmail
Channel gmail-inbox
Channel gmail-sent
Channel gmail-tags

IMAPStore uni-remote
Host outlook.office365.com
User me@uni.nl
AuthMechs XOAUTH2
PassCmd "+~/token.py"

MaildirStore uni-local
Path /m/uni/
Inbox /m/uni/Inbox

Channel uni-inbox
Far :uni-remote:
Near :uni-local:
Patterns INBOX
`

func TestParseAndTargets(t *testing.T) {
	config, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(config.AccountNames(), ","); got != "gmail,uni-remote" {
		t.Fatalf("accounts = %q", got)
	}
	if got := strings.Join(config.ChannelsFor("gmail"), ","); got != "gmail-inbox,gmail-sent,gmail-tags" {
		t.Fatalf("channels = %q (group lines must not become channels)", got)
	}
	uni := config.Accounts["uni-remote"]
	if !uni.UsesXOAUTH2() || uni.PassCmd != "~/token.py" || uni.Address() != "outlook.office365.com:993" {
		t.Fatalf("uni = %+v", uni)
	}
	cases := []struct{ mailbox, spec string }{
		{"INBOX", "gmail-inbox"},
		{"[Gmail]/Sent Mail", "gmail-sent"},
		{"@Reply", "gmail-tags:@Reply"},
	}
	for _, c := range cases {
		target, ok := config.RemoteTarget("gmail", c.mailbox)
		if !ok || target.Spec != c.spec {
			t.Errorf("remote %s = %+v %v, want %s", c.mailbox, target, ok, c.spec)
		}
	}
	if _, ok := config.RemoteTarget("gmail", "Other"); ok {
		t.Error("unsynced mailbox matched")
	}
	local := []struct{ dir, spec string }{
		{"/m/gmail/Inbox", "gmail-inbox"},
		{"/m/gmail/Sent", "gmail-sent"},
		{"/m/gmail/@Waiting", "gmail-tags:@Waiting"},
		{"/m/uni/Inbox/", "uni-inbox"},
	}
	for _, c := range local {
		target, ok := config.LocalTarget(c.dir)
		if !ok || target.Spec != c.spec {
			t.Errorf("local %s = %+v %v, want %s", c.dir, target, ok, c.spec)
		}
	}
	if dir, ok := config.LocalDir("gmail", "INBOX"); !ok || filepath.Clean(dir) != "/m/gmail/Inbox" {
		t.Errorf("inbox dir = %q", dir)
	}
	if dir, ok := config.LocalDir("gmail", "@Reply"); !ok || dir != "/m/gmail/@Reply" {
		t.Errorf("reply dir = %q", dir)
	}
}

func TestPatterns(t *testing.T) {
	if !matchPatterns([]string{"*", "!Spam"}, "Inbox") || matchPatterns([]string{"*", "!Spam"}, "Spam") {
		t.Error("negation")
	}
	if matchPatterns([]string{"%"}, "a/b") || !matchPatterns([]string{"*"}, "a/b") {
		t.Error("hierarchy")
	}
}

// The MacBook's mutt-wizard config: indented keys, one channel per account
// that matches every folder, names with @ in them.
const macSample = `IMAPAccount gmail
  Host imap.gmail.com
  Port 993
  User me@gmail.com
  PassCmd "pass me@gmail.com"
  TLSType IMAPS

IMAPStore gmail-remote
  Account gmail

MaildirStore gmail-local
  Path /m/me@gmail.com/
  Inbox /m/me@gmail.com/INBOX
  SubFolders Verbatim

Channel gmail
  Far :gmail-remote:
  Near :gmail-local:
  Patterns * !"[Gmail]/All Mail" !"*virtual*"

IMAPAccount hello@example.com
  Host imap.mailhost.example
  User hello@example.com

IMAPStore hello@example.com-remote
  Account hello@example.com

MaildirStore hello@example.com-local
  Path /m/hello@example.com/
  Inbox /m/hello@example.com/INBOX

Channel hello@example.com
  Far :hello@example.com-remote:
  Near :hello@example.com-local:
  Patterns *
`

func TestMacStyleConfig(t *testing.T) {
	config, err := Parse(strings.NewReader(macSample))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(config.AccountNames(), ","); got != "gmail,hello@example.com" {
		t.Fatalf("accounts = %q", got)
	}
	for mailbox, spec := range map[string]string{"INBOX": "gmail:INBOX", "@Reply": "gmail:@Reply", "[Gmail]/Sent Mail": "gmail:[Gmail]/Sent Mail"} {
		if target, ok := config.RemoteTarget("gmail", mailbox); !ok || target.Spec != spec {
			t.Errorf("remote %s = %+v %v", mailbox, target, ok)
		}
	}
	if _, ok := config.RemoteTarget("gmail", "[Gmail]/All Mail"); ok {
		t.Error("All Mail is excluded")
	}
	for dir, spec := range map[string]string{"/m/me@gmail.com/INBOX": "gmail:INBOX", "/m/hello@example.com/@Waiting": "hello@example.com:@Waiting"} {
		if target, ok := config.LocalTarget(dir); !ok || target.Spec != spec {
			t.Errorf("local %s = %+v %v", dir, target, ok)
		}
	}
	if dir, _ := config.LocalDir("hello@example.com", "INBOX"); dir != "/m/hello@example.com/INBOX" {
		t.Errorf("inbox dir = %q", dir)
	}
}

func TestRemoteMailbox(t *testing.T) {
	config, _ := Parse(strings.NewReader(sample))
	for dir, want := range map[string]string{
		"/m/gmail/Inbox":  "gmail INBOX",
		"/m/gmail/Sent":   "gmail [Gmail]/Sent Mail",
		"/m/gmail/@Reply": "gmail @Reply",
		"/m/uni/Inbox":    "uni-remote INBOX",
	} {
		account, mailbox, ok := config.RemoteMailbox(dir)
		if !ok || account+" "+mailbox != want {
			t.Errorf("%s = %q %q %v, want %s", dir, account, mailbox, ok, want)
		}
	}
	mac, _ := Parse(strings.NewReader(macSample))
	if account, mailbox, ok := mac.RemoteMailbox("/m/hello@example.com/@Waiting"); !ok || account != "hello@example.com" || mailbox != "@Waiting" {
		t.Errorf("mac = %q %q %v", account, mailbox, ok)
	}
}

// mbsync accepts any whitespace between a key and its value; a tab used to
// leave the whole line as an unknown key and the setting silently unread.
func TestTabSeparatedKeys(t *testing.T) {
	config, err := Parse(strings.NewReader("IMAPAccount\ttab\nHost\timap.example.org\nPort\t1993\nUser\tme\nPass\tx\n\nIMAPStore\ttab-remote\nAccount\ttab\n\nMaildirStore\ttab-local\nPath\t/m/tab/\nInbox\t/m/tab/Inbox\n\nChannel\ttab\nFar\t:tab-remote:\nNear\t:tab-local:\nPatterns\tINBOX\n"))
	if err != nil {
		t.Fatal(err)
	}
	account := config.Accounts["tab"]
	if account == nil || account.Host != "imap.example.org" || account.Address() != "imap.example.org:1993" || account.User != "me" {
		t.Fatalf("account = %+v", account)
	}
	if target, ok := config.LocalTarget("/m/tab/Inbox"); !ok || target.Spec != "tab" {
		t.Fatalf("inbox target = %+v %v", target, ok)
	}
}

func TestSubFoldersAndCertificateFile(t *testing.T) {
	config, err := Parse(strings.NewReader(`IMAPAccount a
Host h
CertificateFile "/etc/ssl/private-ca.pem"

IMAPStore a-remote
Account a

MaildirStore plain
Path /m/plain/

MaildirStore verbatim
SubFolders Verbatim
Path /m/v/

MaildirStore plusplus
SubFolders Maildir++
Path /m/pp/

Channel c
Far :a-remote:
Near :plain:
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Accounts["a"].CertificateFile; got != "/etc/ssl/private-ca.pem" {
		t.Errorf("CertificateFile = %q", got)
	}
	for name, want := range map[string]bool{"plain": true, "verbatim": true, "plusplus": false} {
		if got := config.MaildirStores[name].Verbatim(); got != want {
			t.Errorf("%s Verbatim() = %v, want %v", name, got, want)
		}
	}
	if got := config.MaildirStores["plusplus"].SubFolders; got != "Maildir++" {
		t.Errorf("SubFolders = %q", got)
	}
}

func TestTrueCaseUsesTheSpellingOnDisk(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "University", "INBOX", "cur"), 0o700)
	// On a case-insensitive disk ".../Inbox" opens INBOX; the name must not
	// reach mbsync as "Inbox". (On Linux the lookup is exact anyway.)
	if got := trueCase(filepath.Join(dir, "University", "Inbox")); got != filepath.Join(dir, "University", "INBOX") {
		t.Fatalf("trueCase = %q", got)
	}
	if got := trueCase(filepath.Join(dir, "University", "INBOX", "cur")); got != filepath.Join(dir, "University", "INBOX", "cur") {
		t.Fatalf("exact name changed: %q", got)
	}
	config, _ := Parse(strings.NewReader("IMAPAccount university\nHost h\nUser u\nPass p\n\nIMAPStore r\nAccount university\n\nMaildirStore l\nPath " + dir + "/University/\nInbox " + dir + "/University/INBOX\n\nChannel university\nFar :r:\nNear :l:\nPatterns *\n"))
	if target, ok := config.LocalTarget(filepath.Join(dir, "University", "Inbox")); !ok || target.Spec != "university:INBOX" {
		t.Fatalf("target = %+v %v, want university:INBOX", target, ok)
	}
}
