<?php

/*
| CF-18: only the panel domain, and the origins in CF_CORS_ORIGINS, can call the API from a
| browser. The web panel is on the panel domain, so it does not need CORS.
*/
return [
    'paths' => ['api/*'],
    'allowed_methods' => ['GET', 'POST', 'PUT', 'PATCH', 'DELETE'],
    'allowed_origins' => array_values(array_unique(['https://'.env('CF_PANEL_DOMAIN', 'panel.cloudfactory.io'), ...array_filter(explode(',', env('CF_CORS_ORIGINS', '')))])),
    'allowed_origins_patterns' => [],
    'allowed_headers' => ['Authorization', 'Content-Type', 'Accept'],
    'exposed_headers' => [],
    'max_age' => 3600,
    'supports_credentials' => false,
];
