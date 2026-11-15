// Puts provision.js in the code of the PGP plugin (index.js), and calls it when the plugin
// starts. The build of the webmail image runs this script. It stops when the code of the
// plugin does not have the expected places: a new release of the plugin needs a check.
//   node patch.mjs index.js provision.js
// SPDX-License-Identifier: GPL-3.0-or-later
import { readFileSync, writeFileSync } from "node:fs";

const [file, addition] = process.argv.slice(2);
let code = readFileSync(file, "utf8");
const provision = readFileSync(addition, "utf8");

function once(anchor, replacement) {
  const first = code.indexOf(anchor);
  if (first < 0 || code.indexOf(anchor, first + 1) >= 0) {
    console.error(`patch.mjs: the plugin code does not have one place for: ${anchor}`);
    process.exit(1);
  }
  code = code.replace(anchor, replacement);
}

once("async function activate(api) {", provision + "\nasync function activate(api) {");
once(
  'api.log.info("OpenPGP plugin activated with memory-only session management.");',
  'api.log.info("OpenPGP plugin activated with memory-only session management.");\n' +
    "  try {\n    if (await cfProvisionKeys(api)) locked = false;\n  } catch (err) {\n    api.log.error(\"CloudFactory: the keys of the mailbox\", err);\n  }",
);
writeFileSync(file, code);
