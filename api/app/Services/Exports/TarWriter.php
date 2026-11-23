<?php

namespace App\Services\Exports;

/**
 * The parts of a tar archive (POSIX.1-2001, "pax"). The archive of an account export has only
 * regular files. A path of more than 100 bytes and a file of 8 GiB or more get an extended
 * header ("x") with the path or the size, so that tar, bsdtar and the archive packages of Go
 * and Python read them.
 *
 * An instance writes files with their content in a string to a stream, with no end of the
 * archive. The panel writes the small files of an export in this way (members.tar), and adds
 * the large parts when it sends the archive (ArchiveStream).
 */
class TarWriter
{
    public const BLOCK = 512;

    /** The largest size in the 11 octal digits of a ustar header. */
    private const MAX_OCTAL_SIZE = 0o77777777777;

    /** @param resource $out */
    public function __construct(private $out) {}

    /** Adds a file with its content in a string. */
    public function addString(string $path, string $content, int $mtime): void
    {
        $bytes = self::header($path, strlen($content), $mtime).$content.str_repeat("\0", self::padding(strlen($content)));
        if (fwrite($this->out, $bytes) !== strlen($bytes)) {
            throw new \RuntimeException('The panel could not write the archive. Its disk is possibly full.');
        }
    }

    /** The bytes of the header of a file, with the extended header when the file needs one. */
    public static function header(string $path, int $size, int $mtime): string
    {
        $pax = [];
        $name = $path;
        if (strlen($path) > 100 || preg_match('/[^\x20-\x7e]/', $path)) {
            $pax['path'] = $path;
            $name = substr((string) preg_replace('/[^\x20-\x7e]/', '_', $path), -100);
        }
        if ($size > self::MAX_OCTAL_SIZE) {
            $pax['size'] = (string) $size;
        }
        $out = '';
        if ($pax) {
            $records = '';
            foreach ($pax as $key => $value) {
                $records .= self::paxRecord($key, $value);
            }
            $out .= self::ustar('PaxHeaders/'.substr($name, -80), strlen($records), $mtime, 'x').$records.str_repeat("\0", self::padding(strlen($records)));
        }

        return $out.self::ustar($name, min($size, self::MAX_OCTAL_SIZE), $mtime, '0');
    }

    /** The zero bytes after a file of $size bytes, to the end of its last block. */
    public static function padding(int $size): int
    {
        return (self::BLOCK - $size % self::BLOCK) % self::BLOCK;
    }

    /** The end of the archive: two empty blocks. */
    public static function end(): string
    {
        return str_repeat("\0", self::BLOCK * 2);
    }

    private static function ustar(string $name, int $size, int $mtime, string $type): string
    {
        $header = str_pad($name, 100, "\0")
            .sprintf('%07o', 0o644)."\0"
            .sprintf('%07o', 0)."\0"
            .sprintf('%07o', 0)."\0"
            .sprintf('%011o', $size)."\0"
            .sprintf('%011o', $mtime)."\0"
            .'        '
            .$type
            .str_repeat("\0", 100)
            ."ustar\0".'00'
            .str_repeat("\0", 32 + 32 + 8 + 8 + 155 + 12);
        $sum = array_sum(array_map('ord', str_split($header)));

        return substr_replace($header, sprintf('%06o', $sum)."\0 ", 148, 8);
    }

    /** One record of an extended header: "<length> <key>=<value>\n", with the length of the record. */
    private static function paxRecord(string $key, string $value): string
    {
        $body = " {$key}={$value}\n";
        $length = strlen($body) + 1;
        while (strlen($length.$body) !== $length) {
            $length = strlen($length.$body);
        }

        return $length.$body;
    }
}
