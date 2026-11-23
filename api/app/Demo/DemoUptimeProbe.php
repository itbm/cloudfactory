<?php

namespace App\Demo;

use App\Models\UptimeCheck;
use App\Services\Plugins\Outbound;
use App\Services\Uptime\Probe;
use App\Services\Uptime\ProbeResult;
use Illuminate\Support\Carbon;

/**
 * The uptime checks of the demonstration mode. The panel sends no request. Each sample website
 * answers with HTTP status 200 and a short page with its name. A path with "fail" in it gives
 * no answer, and a check fails when the page does not have its text.
 */
class DemoUptimeProbe extends Probe
{
    public function __construct()
    {
        parent::__construct(new Outbound);
    }

    public function run(iterable $checks): array
    {
        $out = [];
        foreach ($checks as $check) {
            $out[$check->id] = self::answer($check, now());
        }

        return $out;
    }

    public static function answer(UptimeCheck $check, Carbon $at): ProbeResult
    {
        $name = (string) $check->domain?->name;
        if (str_contains($check->path, 'fail')) {
            return ProbeResult::failed("No answer in {$check->timeout_seconds} seconds.", networkError: true);
        }
        $ms = self::latency($name.$check->path, $at);
        if ($check->expected_status !== null && $check->expected_status !== 200) {
            return ProbeResult::failed("HTTP status 200. The check expects {$check->expected_status}.", status: 200, responseMs: $ms);
        }
        if (($check->keyword ?? '') !== '' && ! str_contains(self::page($name), $check->keyword)) {
            return ProbeResult::failed("The answer does not have the text \"{$check->keyword}\".", status: 200, responseMs: $ms);
        }

        return new ProbeResult(true, 200, $ms);
    }

    /** The page of a sample website. */
    public static function page(string $name): string
    {
        return "<!doctype html><title>{$name}</title><h1>{$name}</h1><p>Welcome.</p>";
    }

    /** A response time that changes with the time of the day, and that is the same for the same values. */
    public static function latency(string $seed, Carbon $at): int
    {
        $base = 70 + crc32($seed) % 180;
        $cycle = 0.8 + 0.35 * sin(($at->hour + $at->minute / 60 - 9) / 24 * 2 * M_PI);
        $noise = (crc32($seed.$at->format('YmdHi')) % 41) - 20;

        return max(20, (int) round($base * $cycle + $noise));
    }
}
