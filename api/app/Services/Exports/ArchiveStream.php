<?php

namespace App\Services\Exports;

/**
 * An archive that the panel sends from pieces: bytes in memory and files on the disk. The
 * panel does not copy the parts of an export into one file. It sends the tar headers and the
 * files one after the other, so that the disk keeps one copy of each part, and a download can
 * start at an offset (an HTTP range).
 */
class ArchiveStream
{
    /** @var list<array{data: ?string, file: ?string, bytes: int}> */
    private array $segments = [];

    private int $size = 0;

    public function data(string $bytes): void
    {
        if ($bytes !== '') {
            $this->segments[] = ['data' => $bytes, 'file' => null, 'bytes' => strlen($bytes)];
            $this->size += strlen($bytes);
        }
    }

    public function file(string $path, int $bytes): void
    {
        if ($bytes > 0) {
            $this->segments[] = ['data' => null, 'file' => $path, 'bytes' => $bytes];
            $this->size += $bytes;
        }
    }

    public function size(): int
    {
        return $this->size;
    }

    /** True when each file is on the disk with its size. */
    public function available(): bool
    {
        foreach ($this->segments as $s) {
            if ($s['file'] !== null) {
                clearstatcache(true, $s['file']);
                if (! is_file($s['file']) || filesize($s['file']) !== $s['bytes']) {
                    return false;
                }
            }
        }

        return true;
    }

    /**
     * Writes the bytes from $start to $end (both included) to $out.
     *
     * @param  resource  $out
     *
     * @throws \RuntimeException when a file is not available
     */
    public function send($out, int $start, int $end): void
    {
        $offset = 0;
        foreach ($this->segments as $s) {
            $from = max($start - $offset, 0);
            $to = min($end - $offset, $s['bytes'] - 1);
            $offset += $s['bytes'];
            if ($from > $to) {
                if ($offset > $end) {
                    break;
                }

                continue;
            }
            $length = $to - $from + 1;
            if ($s['data'] !== null) {
                fwrite($out, substr($s['data'], $from, $length));

                continue;
            }
            $in = fopen($s['file'], 'rb');
            if ($in === false) {
                throw new \RuntimeException("The panel cannot open {$s['file']}.");
            }
            try {
                if (stream_copy_to_stream($in, $out, $length, $from) !== $length) {
                    throw new \RuntimeException("The panel cannot read {$s['file']}.");
                }
            } finally {
                fclose($in);
            }
        }
    }
}
