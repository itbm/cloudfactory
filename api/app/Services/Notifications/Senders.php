<?php

namespace App\Services\Notifications;

use App\Models\Customer;
use App\Models\MailServer;
use App\Models\User;
use App\Services\Branding;

/**
 * Who sends an email to a user. The customers of a reseller get emails with the brand of the
 * reseller, through the SMTP server of the reseller when it has one. All other users get
 * emails from the panel: administrators, resellers and the direct customers of the panel.
 */
class Senders
{
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
        // The same brand as the panel: the brand of the reseller, else the default brand that
        // the administrator sets.
        $brand = Branding::forCustomer($reseller);

        return [
            'name' => (string) $brand['name'],
            'colour' => (string) $brand['colour'],
            'url' => $brand['panelDomain'] ? 'https://'.$brand['panelDomain'] : (string) config('app.url'),
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
