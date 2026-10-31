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
        // CF-10: the addresses from which the agent on the panel server calls the API directly.
        // 169.254.77.1 is the private service address of the panel server (install.sh).
        'direct_addresses' => array_filter(explode(',', env('CF_AGENT_DIRECT_ADDRESSES', '127.0.0.1,::1,169.254.77.1'))),
    ],

    'registry' => env('CF_REGISTRY', 'ghcr.io/cloudfactory'),

    // FR-1101 and FR-1110: the release public key. The panel accepts a new tag of an official
    // image only when the tag has a good signature from this key.
    'cosign_public_key' => env('CF_COSIGN_PUBLIC_KEY', '/run/cloudfactory/cosign.pub'),

    'panel_domain' => env('CF_PANEL_DOMAIN', 'panel.cloudfactory.io'),

    // R-14: the address of the installation script in the "Add server" command. Each GitHub
    // release has the script with its signature (docs/installation.md).
    'install_url' => env('CF_INSTALL_URL', 'https://github.com/itbm/cloudfactory/releases/latest/download/install.sh'),

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
        // CF-02: only a token with this scope, or a session token, can see and change tokens.
        'tokens:read', 'tokens:write',
    ],
];
