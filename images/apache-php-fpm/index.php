<?php
// The default page of a new website. Upload your files to replace it.
header('Content-Type: text/html; charset=utf-8');
?><!doctype html>
<html lang="en-GB">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Website is ready</title>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto; padding: 0 1rem">
<h1>This website is ready</h1>
<p>Cloudfactory made this page. Upload your files with SFTP, or deploy from Git, to replace it.</p>
<p>PHP <?= htmlspecialchars(PHP_VERSION) ?></p>
</body>
</html>
