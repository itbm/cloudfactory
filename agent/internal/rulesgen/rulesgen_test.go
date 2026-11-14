package rulesgen

import (
	"strings"
	"testing"
)

func TestGenerate(t *testing.T) {
	goCode, phpCode, err := Generate([]byte(`{
		"fragments": {"word": "[a-z]+"},
		"rules": {"twoWords": {"what": "name", "doc": "Two words.", "pattern": "^{word}/{word}$", "not": ["^x"], "maxLength": 9, "empty": true, "message": "Enter two words."}},
		"sets": {"reservedNames": {"doc": "Names.", "values": ["a", "it's"]}},
		"limits": {"words": {"doc": "The largest number.", "value": 2}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"func TwoWords(s string) error { return ruleTwoWords.check(s) }",
		"pattern:   regexp.MustCompile(`^[a-z]+/[a-z]+$`)",
		`"twoWords": &ruleTwoWords`,
		`var reservedNames = []string{"a", "it's"}`,
		"const MaxWords = 2",
	} {
		if !strings.Contains(string(goCode), want) {
			t.Errorf("the Go file does not have: %s\n%s", want, goCode)
		}
	}
	for _, want := range []string{
		// One pattern: empty, or (length, not, pattern). "/" has an escape, and D ends the text at "$".
		`public const TWO_WORDS = '/^(?:\\z|(?=[\\s\\S]{0,9}\\z)(?![\\s\\S]*?(?:^x))(?:^[a-z]+\\/[a-z]+$))/D';`,
		`'twoWords' => self::TWO_WORDS,`,
		`'twoWords' => 'Enter two words.',`,
		`public const RESERVED_NAMES = ['a', 'it\'s'];`,
		"public const MAX_WORDS = 2;",
	} {
		if !strings.Contains(string(phpCode), want) {
			t.Errorf("the PHP file does not have: %s\n%s", want, phpCode)
		}
	}
}

func TestGenerateRefusesARuleThatTheTwoLanguagesReadDifferently(t *testing.T) {
	rule := func(pattern string) string {
		return `{"rules": {"a": {"what": "a", "doc": "A.", "message": "A.", "pattern": "` + pattern + `"}}}`
	}
	for name, schema := range map[string]string{
		"a lookahead, which Go does not have": rule("^(?=a)a$"),
		"no anchors":                          rule("a"),
		"a fragment that does not exist":      rule("^{none}$"),
		"a field that does not exist":         `{"rules": {}, "extra": 1}`,
		"no message":                          `{"rules": {"a": {"what": "a", "doc": "A.", "pattern": "^a$"}}}`,
	} {
		if _, _, err := Generate([]byte(schema)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
