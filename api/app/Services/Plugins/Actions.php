<?php

namespace App\Services\Plugins;

/**
 * Actions. A plugin adds buttons to the page of a website, a server or a customer. When a user
 * selects a button, the panel sends a signed request to the plugin. The request has a token of
 * the user for 15 minutes, with the scopes of the plugin, so that the plugin acts with the
 * rights of that user. The plugin answers {"message": "...", "url": "..."}: the web panel shows
 * the message, and a link to the URL when the URL is on the address of the plugin.
 */
class Actions
{
    /** The pages that can have an action. */
    public const TARGETS = ['website', 'server', 'customer'];

    /** The time in seconds that the panel waits for the plugin. */
    public const TIMEOUT = 10;

    /** The longest message that the panel shows. */
    public const MESSAGE_LENGTH = 500;

    /** The token name of an action. The ID of the plugin follows it. */
    public const TOKEN_PREFIX = 'Plugin action ';
}
