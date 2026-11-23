<?php

namespace App\Services\Uptime;

/** The result of one request of an uptime check. */
final readonly class ProbeResult
{
    /**
     * @param  bool  $ok  true when the answer is the answer that the check expects
     * @param  ?int  $status  the HTTP status, or null when there was no answer
     * @param  ?int  $responseMs  the time to the full answer, in milliseconds
     * @param  ?string  $error  why the request failed, for people
     * @param  bool  $networkError  true when the panel got no answer: no address, no connection
     *                              or no answer in the time limit. The cause can be the
     *                              network of the panel.
     */
    public function __construct(
        public bool $ok,
        public ?int $status = null,
        public ?int $responseMs = null,
        public ?string $error = null,
        public bool $networkError = false,
    ) {}

    public static function failed(string $error, bool $networkError = false, ?int $status = null, ?int $responseMs = null): self
    {
        return new self(false, $status, $responseMs, mb_substr($error, 0, 300), $networkError);
    }
}
