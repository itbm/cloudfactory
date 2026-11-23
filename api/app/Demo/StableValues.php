<?php

namespace App\Demo;

use Illuminate\Database\Eloquent\Model;
use Illuminate\Support\Carbon;
use Illuminate\Support\Str;
use Symfony\Component\Uid\Ulid;

/**
 * Gives the same IDs and random strings on each run of the fixtures command, so that a new run
 * changes the fixture files only where the API or the sample data changed.
 *
 * Each table, and each other caller, has its own sequence. The n-th ULID of a table is the same
 * on each run, also when the sample data gets rows in a different table. The ULIDs of a table
 * keep the order in which the rows were made, as real ULIDs do.
 */
class StableValues
{
    private const CROCKFORD = '0123456789ABCDEFGHJKMNPQRSTVWXYZ';

    public static function start(): void
    {
        $counts = [];
        Str::createUlidsUsing(function () use (&$counts): Ulid {
            // [0] is this function, [1] is call_user_func, [2] is Str::ulid() and [3] is the code that wants the ULID.
            $trace = debug_backtrace(DEBUG_BACKTRACE_PROVIDE_OBJECT, 4);
            $scope = self::scope($trace[3] ?? []);
            $n = $counts[$scope] = ($counts[$scope] ?? 0) + 1;
            // Str::ulid($time) gives a ULID with that time. The others have the time of the fixtures.
            $time = $trace[2]['args'][0] ?? null;
            $ms = (int) ($time === null ? Carbon::now() : Carbon::parse($time))->getPreciseTimestamp(3);

            return new Ulid(self::base32($ms, 10).self::base32(crc32($scope) & 0x3FFFFFFF, 6).self::base32($n, 10));
        });
        Str::createRandomStringsUsing(function (int $length) use (&$counts): string {
            // [0] is this function, [1] is Str::random() and [2] is the code that wants the string.
            $scope = 'random '.self::scope(debug_backtrace(DEBUG_BACKTRACE_PROVIDE_OBJECT, 3)[2] ?? []);
            $n = $counts[$scope] = ($counts[$scope] ?? 0) + 1;
            $string = '';
            for ($block = 0; strlen($string) < $length; $block++) {
                $string .= str_replace(['/', '+', '='], '', base64_encode(hash('sha256', "{$scope} {$n} {$block}", true)));
            }

            return substr($string, 0, $length);
        });
    }

    public static function stop(): void
    {
        Str::createUlidsNormally();
        Str::createRandomStringsNormally();
    }

    /**
     * The sequence of a caller: the table of a model that makes its own key, or the function.
     *
     * @param  array<string, mixed>  $frame
     */
    private static function scope(array $frame): string
    {
        $object = $frame['object'] ?? null;

        return $object instanceof Model ? $object->getTable() : ($frame['class'] ?? '').'::'.($frame['function'] ?? '');
    }

    private static function base32(int $value, int $length): string
    {
        $text = '';
        for ($i = 0; $i < $length; $i++) {
            $text = self::CROCKFORD[$value & 31].$text;
            $value >>= 5;
        }

        return $text;
    }
}
