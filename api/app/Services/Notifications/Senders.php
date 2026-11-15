<?php

namespace App\Services\Notifications;

use App\Models\Customer;
use App\Models\MailServer;
use App\Models\User;

/**
 * Who sends an email to a user. The customers of a reseller get emails with the brand of the
 * reseller, through the SMTP server of the reseller when it has one. All other users get
 * emails from the panel: administrators, resellers and the direct customers of the panel.
 */
class Senders
{
    public const DEFAULT_NAME = 'CloudFactory';

    public const DEFAULT_COLOUR = '#1d5fd6';

    /** The reseller whose brand a user sees, or null for the panel. */
    public static function brandOwner(User $user): ?Customer
    {
        $reseller = $user->customer?->reseller;

        return $reseller && $reseller->role === 'reseller' ? $reseller : null;
    }

    /**
     * The name, the colour and the address of the panel for a brand.
     *
     * @return array{name: string, colour: string, url: string}
     */
    public static function brand(?Customer $reseller): array
    {
        $branding = $reseller?->branding ?? [];
        $domain = $branding['panelDomain'] ?? config('cloudfactory.panel_domain');

        return [
            'name' => (string) ($branding['name'] ?? $reseller?->name ?? self::DEFAULT_NAME),
            'colour' => preg_match('/^#[0-9a-fA-F]{6}$/', (string) ($branding['colour'] ?? '')) ? $branding['colour'] : self::DEFAULT_COLOUR,
            'url' => $domain ? 'https://'.$domain : (string) config('app.url'),
        ];
    }

    /**
     * The SMTP server for the emails of a brand: the server of the reseller, else the server
     * of the panel. Null when no server is on.
     */
    public static function server(?Customer $reseller): ?MailServer
    {
        if ($reseller && ($own = MailServer::of($reseller)) && $own->enabled) {
            return $own;
        }
        $panel = MailServer::of(null);

        return $panel && $panel->enabled ? $panel : null;
    }
}
