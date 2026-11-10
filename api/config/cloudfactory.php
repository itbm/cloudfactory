<?php

return [
    // The panel calls each agent with mutual TLS (spec section 6.4, FR-1706).
    'agent' => [
        'client_certificate' => env('CF_AGENT_CLIENT_CERT', storage_path('pki/panel.crt')),
        'client_key' => env('CF_AGENT_CLIENT_KEY', storage_path('pki/panel.key')),
        // The panel keeps the certificate of each agent here (from the join request).
        'certificates' => env('CF_AGENT_CERTIFICATES', storage_path('pki/agents')),
        'timeout' => (int) env('CF_AGENT_TIMEOUT', 10),
        // Audit finding R11: a server that does not answer must not hold the queue for long.
        'connect_timeout' => (int) env('CF_AGENT_CONNECT_TIMEOUT', 3),
        // The latest agent version. Servers with an older agent show "Agent update ready".
        'latest_version' => env('CF_AGENT_LATEST', '0.1.0'),
        // For development only (CF-10): the addresses from which an agent can call the API with
        // its token and no client certificate. Keep this empty on a server. There, the agent on
        // the panel server uses the HTTPS port of the API and shows its certificate.
        'direct_addresses' => array_values(array_filter(explode(',', (string) env('CF_AGENT_DIRECT_ADDRESSES', '')))),
    ],

    // Audit finding S12: the default is the registry of the project. The panel unit sets
    // CF_REGISTRY to the registry of the installation.
    'registry' => env('CF_REGISTRY', 'ghcr.io/itbm'),

    // FR-1101 and FR-1110: the release public key. The panel accepts a new tag of an official
    // image only when the tag has a good signature from this key.
    'cosign_public_key' => env('CF_COSIGN_PUBLIC_KEY', '/run/cloudfactory/cosign.pub'),

    'panel_domain' => env('CF_PANEL_DOMAIN', 'panel.cloudfactory.io'),

    // R-14: the address of the installation script in the "Add server" command. Each GitHub
    // release has the script with its signature (docs/installation.md).
    'install_url' => env('CF_INSTALL_URL', 'https://github.com/itbm/cloudfactory/releases/latest/download/install.sh'),

    // Audit finding S5: the maximum number of domains of one website. Each domain starts a
    // certificate order, and Let's Encrypt limits the orders of a server.
    'domains_per_website' => (int) env('CF_DOMAINS_PER_WEBSITE', 50),

    // FR-902: the default retention policy.
    'backups' => [
        'daily' => 7,
        'weekly' => 4,
        'monthly' => 6,
        'time' => '03:00',
        'target' => env('CF_BACKUP_TARGET', ''),
    ],

    // Audit finding R8: the number of manual backups that the panel keeps for each website.
    // The retention policy above is only for the scheduled backups.
    'manual_backups' => (int) env('CF_MANUAL_BACKUPS', 10),

    // Audit finding R8: the number of days that the backups of a deleted website stay in the
    // repository. In this time, an administrator can restore them with restic.
    'deleted_website_backup_days' => (int) env('CF_DELETED_WEBSITE_BACKUP_DAYS', 30),

    // FR-104: the scopes that an API token can have.
    'scopes' => [
        'websites:read', 'websites:write',
        'databases:read', 'databases:write',
        'backups:read', 'backups:write',
        'servers:read', 'servers:write',
        'images:read', 'images:write',
        'customers:read', 'customers:write',
        // Audit finding B5: a token with users:write can make users and reset passwords for
        // customers, and thus get the access of those users. It cannot change administrators.
        'users:read', 'users:write',
        'audit:read',
        // CF-02: only a token with this scope, or a session token, can see and change tokens.
        'tokens:read', 'tokens:write',
    ],
];
