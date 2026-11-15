"""The welcome page of a new Python website. Upload your application to replace it."""
# SPDX-License-Identifier: AGPL-3.0-only
import os
import platform
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PAGE = f"""<!doctype html>
<html lang="en-GB">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Website is ready</title>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; padding: 0 1rem">
<h1>This website is ready</h1>
<p>Cloudfactory made this page. Upload your application with an <code>app.py</code>, or deploy it from Git.</p>
<p>Python {platform.python_version()}</p>
</body>
</html>
""".encode()


class Welcome(BaseHTTPRequestHandler):
    def do_HEAD(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(PAGE)))
        self.end_headers()

    def do_GET(self):
        self.do_HEAD()
        self.wfile.write(PAGE)

    def log_message(self, format, *args):
        pass


ThreadingHTTPServer((os.environ.get("HOST", "0.0.0.0"), int(os.environ.get("PORT", "8080"))), Welcome).serve_forever()
