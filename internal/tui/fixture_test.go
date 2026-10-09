package tui

import "github.com/aronvandepol/mailday/internal/config"

// testConfig is the fictional user of the compose tests.
var testConfig = config.Config{Name: "Sam de Vries", Calendar: config.Calendar{HomeZone: "Europe/Amsterdam"},
	Mail: config.Mail{BoxKeys: map[string]string{"@ESS": "a", "@Admin": "d", "@Conferences": "c", "@Grants": "s", "@Lab": "k",
		"@Journal": "h", "@EC": "l", "@PeerReview": "v", "@PhD": "p", "@Research": "e", "@Teaching": "t", "@Travel": "j"}},
	Identity: []config.Identity{
		{Accounts: []string{"gmail"}, Name: "Sam de Vries", Address: "sam.devries@gmail.example",
			Signature: []string{"Sam"}},
		{Accounts: []string{"uni", "University"}, Name: "Sam de Vries", Address: "s.de.vries@hum.uni.example", Aliases: []string{"vriessde@staff.uni.example"},
			Signature:      []string{"Sam de Vries", "PhD Candidate · Example University", "Department of Example Studies", "Example University Centre for Digital Humanities", "sam.example.org"},
			ShortSignature: []string{"Sam de Vries", "PhD Candidate · Example University"}},
		{Accounts: []string{"home", "samdevries"}, Name: "Sam de Vries", Address: "hello@samdevries.example", SaveSent: true,
			Signature: []string{"Sam de Vries", "samdevries.example"}},
		{Accounts: []string{"society"}, Name: "Sam de Vries", Address: "web@society.example", SaveSent: true,
			Signature: []string{"Sam de Vries", "Web editor · Example Society"}},
	}}
