<?php

return [
    // The panel calls each agent with mutual TLS (spec section 6.4, FR-1706).
    'agent' => [
        'client_certificate' => env('CF_AGENT_CLIENT_CERT', storage_path('pki/panel.crt')),
        'client_key' => env('CF_AGENT_CLIENT_KEY', storage_path('pki/panel.key')),
        // The panel keeps the certificate of each agent here (from the join request).
        'certificates' => env('CF_AGENT_CERTIFICATES', storage_path('pki/agents')),
        'timeout' => (int) env('CF_AGENT_TIMEOUT', 10),
        // The latest agent version. Servers with an older agent show "Agent update ready".
        'latest_version' => env('CF_AGENT_LATEST', '0.1.0'),
    ],

    'registry' => env('CF_REGISTRY', 'ghcr.io/cloudfactory'),

    'panel_domain' => env('CF_PANEL_DOMAIN', 'panel.cloudfactory.io'),

    // FR-902: the default retention policy.
    'backups' => [
        'daily' => 7,
        'weekly' => 4,
        'monthly' => 6,
        'time' => '03:00',
        'target' => env('CF_BACKUP_TARGET', ''),
    ],

    // FR-104: the scopes that an API token can have.
    'scopes' => [
        'websites:read', 'websites:write',
        'databases:read', 'databases:write',
        'backups:read', 'backups:write',
        'servers:read', 'servers:write',
        'images:read', 'images:write',
        'customers:read', 'customers:write',
        'audit:read',
    ],
];
