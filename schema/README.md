# Input rules

This directory has the input rules that the panel API (PHP) and the agent (Go) share.

| File                 | Contents                                                       |
| -------------------- | -------------------------------------------------------------- |
| `input-rules.json`   | The rules. This file is the only source of the rules.          |
| `input-vectors.json` | Valid and invalid examples for each rule. The tests of the two components run them. |

Do not change the generated files by hand. Refer to
[docs/development.md](../docs/development.md#input-rules) for the procedure to change a rule.

## Format of `input-rules.json`

### `rules`

Each rule has a name, for example `domain`, and these fields:

| Field       | Necessary | Meaning |
| ----------- | --------- | ------- |
| `what`      | Yes | The name of the value in an error of the agent, for example `the domain "x" is not valid`. |
| `doc`       | Yes | The comment in the generated code. |
| `message`   | Yes | The text that the panel shows to a user who typed a value that is not valid. |
| `pattern`   | Yes | A regular expression. It must start with `^` and end with `$`. |
| `also`      | No  | More regular expressions. The value must agree with each of them. |
| `not`       | No  | Regular expressions that the value must not agree with. |
| `maxLength` | No  | The largest length in bytes. |
| `empty`     | No  | `true` when an empty value is permitted. |
| `echo`      | No  | `false` when the error of the agent must not repeat the value, for example a long key. |
| `go`        | No  | The name of the Go function, if it is not the name of the rule with a capital letter. |

Use only the regular expression syntax of Go (RE2). PHP reads this syntax in the same way. The
generator refuses a pattern that Go cannot read, for example a lookahead. Use `also` and `not`
as an alternative to a lookahead.

`$` agrees only with the end of the value in the two languages. A value with a line feed at the
end is not valid.

### `fragments`

A fragment is a part of a pattern with a name. Write `{name}` in a pattern to use it.

### `sets` and `limits`

A set is a list of text values, for example the reserved database names. A limit is a number,
for example the largest number of arguments of a start command. The generator writes each set
and each limit as a constant in the two languages.

## Format of `input-vectors.json`

Each rule has a list `valid` and a list `invalid`. An example is a string, or an object for a
long value:

```json
{ "repeat": ["ssh-rsa ", ["A", 8184]] }
```

A string in the list is used as it is. A pair repeats its string that number of times.

## Values that are not in this file

Some checks are not patterns. They stay in the code:

- The agent: `validate.PanelURL`, `validate.SftpPort`, `validate.Port` and
  `validate.TenantDatabaseName` (the name must start with the Unix user of the website).
- The panel: the range of each `php.ini` value (`RuntimeController::checkIni`), and the rules
  for values that the panel does not send to the agent.
