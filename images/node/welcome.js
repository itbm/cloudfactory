// The welcome page of a new Node.js website. Upload your application to replace it.
// SPDX-License-Identifier: AGPL-3.0-only
"use strict";
const http = require("node:http");

const page = `<!doctype html>
<html lang="en-GB">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Website is ready</title>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; padding: 0 1rem">
<h1>This website is ready</h1>
<p>Cloudfactory made this page. Upload your application with a <code>package.json</code>, or deploy it from Git.</p>
<p>Node.js ${process.versions.node}</p>
</body>
</html>
`;

http
  .createServer((_req, res) => {
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
    res.end(page);
  })
  .listen(Number(process.env.PORT || 8080), process.env.HOST || "0.0.0.0");
