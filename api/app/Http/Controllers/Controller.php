<?php

namespace App\Http\Controllers;

use Illuminate\Http\Request;

abstract class Controller
{
    /** Audit batch 3, finding 3: the page size is from 1 to the maximum. */
    protected static function perPage(Request $request, int $default = 25, int $max = 100): int
    {
        return max(1, min($max, (int) $request->query('perPage', $default)));
    }
}
