<?php

use Illuminate\Support\Facades\Route;

// The panel API has no web pages. The web panel is a separate app (see web/).
Route::get('/', fn () => response()->json(['name' => 'Cloudfactory panel API', 'version' => '0.1.0', 'documentation' => '/openapi.yaml']));
