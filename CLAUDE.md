# Notes for coding agents

## Compatibility while the version is 0.x

While the version of Cloudfactory is 0.x, treat the project as new development with no
installations to keep. Backwards compatibility is not necessary:

- Do not write migrations that move old data, shims, fallbacks or compatibility layers.
- Change the migration that made a table, or add a migration that makes the new form
  directly. Remove the old code, fields, routes and commands in the same change.
- Change the tests, the web fixtures, `openapi/openapi.yaml` and the documentation in the same
  change.

The exception is the pack contract (`cf-pack/1` and `cf-runtime/1`). It is stable, because
publishers outside the project depend on it. Refer to
[docs/packs.md](docs/packs.md#the-stable-contract).

The full rule is in
[docs/development.md](docs/development.md#compatibility-while-the-version-is-0x).

## Rules that the tests enforce

- The panel API and the agent share the input rules in `schema/input-rules.json`. After a
  change, run `cd agent && go run ./cmd/cf-rules`. Do not change the generated files by hand.
- The panel API and the agent share the pack manifest cases in `schema/pack-vectors.json`.
- Each API route must be in `openapi/openapi.yaml` (`api/tests/Feature/OpenApiTest.php`).
- The web tests use the API answers in `web/src/test/fixtures/ROLE`, one file for each request.
  After a change to the API or to `DemoSeeder`, run `php artisan demo:fixtures` in `api/` (with
  `APP_ENV=local`). Add a new request to `api/app/Demo/Fixtures.php` in alphabetical order.
- The Go code of the agent protocol (`agent/gen`) is not in Git. Run `go generate` in `agent/`
  before you build or test the agent, and after a change to `agent.proto`. It needs `buf`.

## Parallel pull requests

Many pull requests are open at the same time. GitHub ignores merge drivers, so the layout of
the files must prevent conflicts. The rules are in
[docs/development.md](docs/development.md#parallel-pull-requests). In short:

- Put one item on each line of a list, and put a new item in alphabetical order or next to the
  related item. Do not add it at the end of the list.
- Tests check the alphabetical order of the agent methods in `agent.proto` and `server.go`, of
  `AgentAction` and of the fixture paths. Put a new method or action in its alphabetical place.
- Do not write comments that name each item of a list.
- Keep the blank line between the items of `docs/status.md`. Change only the item of your
  feature.
- Put new code for a new area in a new file when the language permits it.

## Running the tests

Run only the tests of the files that you change. The CI runs the full suites.

- Panel API: `php artisan test --filter=NameTest` in `api/`. For a full run, add `--parallel`.
- Web panel: `npx vitest run src/pages/name.test.tsx` in `web/`.
- Agent and CLI: `go test ./internal/name/...`, not `./...`.

## Writing

Use British English. Write the documentation in ASD-STE100 Simplified Technical English:
short sentences, the active voice, and one instruction in each step.
