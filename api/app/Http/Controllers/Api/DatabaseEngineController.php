<?php

namespace App\Http\Controllers\Api;

use App\Http\Controllers\Controller;
use App\Services\DatabaseEngines;
use Illuminate\Http\JsonResponse;

/**
 * The database engines of the panel: the built-in engines and the engines of the database
 * packs, with their versions and their features. The web panel shows them in the engine
 * choices.
 */
class DatabaseEngineController extends Controller
{
    public function index(): JsonResponse
    {
        return response()->json(['data' => DatabaseEngines::present()]);
    }
}
