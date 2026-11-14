<?php

return [
    /*
    | The demonstration mode, for development, demonstrations and screenshots. The panel then has
    | no real server: a demo agent in the API completes each job (app/Demo). The demo seeder
    | (database/seeders/DemoSeeder.php) makes the sample data.
    |
    | Never set CF_DEMO on a server with customers. The panel ignores it when APP_ENV is
    | "production".
    */
    'enabled' => (bool) env('CF_DEMO', false),

    // The time for each step of a job, in milliseconds. The tests use 0.
    'step_ms' => (int) env('CF_DEMO_STEP_MS', 700),
];
