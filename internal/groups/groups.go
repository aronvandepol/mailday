// Package groups keeps recipient groups: named lists of addresses that the
// composer expands, so "Board" becomes its seven people. Email has no
// such thing that works everywhere, so they live in a plain text file of
// Mailday's own, shared between machines by Syncthing and easy to edit by hand:
//
//	[Board]
//	Alice Kim <alice@example.org>
//	bob@example.org
package groups

import (
	"bufio"
	"fmt"
	"github.com/aronvandepol/mailday/internal/config"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Group is a name and its people, in the order they were added.
type Group struct {
	Name    string
	Members []mail.Address
}

// Path is the groups file: MAILDAY_GROUPS_FILE, else groups.txt in the
// shared_dir beside the snooze list.
func Path() string {
	if value := os.Getenv("MAILDAY_GROUPS_FILE"); value != "" {
		return value
	}
	return config.SharedPath("groups.txt")
}

const fileHeader = `# Mailday recipient groups. P in Mailday shows them; typing a group's name
# in To, Cc or Bcc fills in everyone. One group per block: its name in
# [brackets], then one address per line ("Name <address>" or just the address).
`

// Load reads the groups file; a missing file is no groups.
func Load(path string) ([]Group, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return Parse(file)
}

// Parse reads the file format. A line that is not an address is an error
// naming its line, so a typo made by hand is found rather than dropped.
func Parse(reader io.Reader) ([]Group, error) {
	var groups []Group
	scanner := bufio.NewScanner(reader)
	number := 0
	for scanner.Scan() {
		number++
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			name := strings.TrimSpace(line[1 : len(line)-1])
			if name == "" {
				return groups, fmt.Errorf("line %d: a group needs a name", number)
			}
			groups = append(groups, Group{Name: name})
		case len(groups) == 0:
			return groups, fmt.Errorf("line %d: %q comes before any [group name]", number, line)
		default:
			address, err := mail.ParseAddress(line)
			if err != nil {
				return groups, fmt.Errorf("line %d: %q is not an address", number, line)
			}
			group := &groups[len(groups)-1]
			if !group.Has(address.Address) {
				group.Members = append(group.Members, *address)
			}
		}
	}
	return groups, scanner.Err()
}

// Format writes groups in the file format, sorted by name.
func Format(groups []Group) string {
	sorted := slices.Clone(groups)
	slices.SortStableFunc(sorted, func(a, b Group) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	var out strings.Builder
	out.WriteString(fileHeader)
	for _, group := range sorted {
		fmt.Fprintf(&out, "\n[%s]\n", group.Name)
		for _, member := range group.Members {
			out.WriteString(FormatMember(member) + "\n")
		}
	}
	return out.String()
}

// FormatMember is one address as it is written in To and in the file.
func FormatMember(member mail.Address) string {
	if member.Name == "" {
		return member.Address
	}
	return (&mail.Address{Name: member.Name, Address: member.Address}).String()
}

// Save writes the file whole and renames it into place, so Syncthing and a
// reader on the other machine never see half of it.
func Save(path string, groups []Group) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".groups-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.WriteString(Format(groups)); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// Update reads the file, changes it and writes it back. The file is small
// and edited by one person, so read-change-write is enough.
func Update(path string, change func([]Group) ([]Group, error)) error {
	groups, err := Load(path)
	if err != nil {
		return err
	}
	groups, err = change(groups)
	if err != nil {
		return err
	}
	return Save(path, groups)
}

// Has reports whether the group holds the address (case aside).
func (g Group) Has(address string) bool {
	return slices.ContainsFunc(g.Members, func(member mail.Address) bool { return strings.EqualFold(member.Address, address) })
}

// Find returns the index of the group with this name (case aside), or -1.
func Find(groups []Group, name string) int {
	return slices.IndexFunc(groups, func(group Group) bool { return strings.EqualFold(group.Name, strings.TrimSpace(name)) })
}

// Add puts members into the named group, making it if needed; addresses it
// already holds are skipped. added counts the new ones.
func Add(groups []Group, name string, members []mail.Address) (out []Group, added int, created bool) {
	name = strings.TrimSpace(name)
	index := Find(groups, name)
	if index < 0 {
		groups = append(groups, Group{Name: name})
		index, created = len(groups)-1, true
	}
	group := &groups[index]
	for _, member := range members {
		if member.Address == "" || group.Has(member.Address) {
			continue
		}
		group.Members = append(group.Members, member)
		added++
	}
	return groups, added, created
}

// Search returns the groups whose name holds every word of query, groups
// whose name starts with it first.
func Search(groups []Group, query string) []Group {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil
	}
	var starts, contains []Group
	for _, group := range groups {
		name := strings.ToLower(group.Name)
		if !allIn(name, words) {
			continue
		}
		if strings.HasPrefix(name, words[0]) {
			starts = append(starts, group)
		} else {
			contains = append(contains, group)
		}
	}
	return append(starts, contains...)
}

func allIn(text string, words []string) bool {
	for _, word := range words {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}
