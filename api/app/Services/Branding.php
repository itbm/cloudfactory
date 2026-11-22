<?php

namespace App\Services;

use App\Enums\AgentAction;
use App\Models\Customer;
use App\Models\Server;
use App\Models\Setting;
use App\Models\User;
use App\Services\Dns\Zones;
use Illuminate\Validation\ValidationException;

/**
 * White label: the brand that each person sees in the panel.
 *
 * - The administrator sets the default brand: the name, the colour, the logo and the support
 *   email of the panel. The administrators, and the customers of the panel itself, see it.
 * - A reseller sets its own brand. The reseller and its customers see only that brand. A field
 *   that the reseller did not set comes from the reseller (its name and its email) or from the
 *   built-in values, never from the default brand of the administrator. Thus a customer of a
 *   reseller never sees the name of the hosting company.
 * - A reseller can have its own panel domain. The edge proxy on the panel server sends it to
 *   the panel and gets a TLS certificate for it (SetPanelBrands). The sign-in page on that
 *   domain shows the brand of the reseller.
 * - Where the panel has DNS hosting, a reseller can give its own host names to the name
 *   servers of the panel, for example ns1.agency.example. The zones of the reseller and of its
 *   customers then have these names in their NS and SOA records.
 * - A reseller can also set a mail host name, for example mail.agency.example. The email
 *   hosting of the panel shows it to the customers of the reseller.
 */
class Branding
{
    /** The built-in name and colour. A new panel uses them. */
    public const NAME = 'Cloudfactory';

    public const COLOUR = '#1d5fd6';

    /** The largest logo: 256 KB, 2048 by 2048 pixels. */
    public const LOGO_BYTES = 262144;

    public const LOGO_PIXELS = 2048;

    /** The image types of a logo. SVG is not allowed, because an SVG file can hold a script. */
    public const LOGO_TYPES = ['image/png', 'image/jpeg', 'image/webp'];

    private const SETTING = 'brand';

    /** The state of the certificate of each reseller panel domain, from the agent. */
    private const CERTIFICATES = 'brand_certificates';

    /**
     * The default brand of the administrator, with the built-in values for each field that the
     * administrator did not set.
     *
     * @return array{name: string, colour: string, logo: ?string, supportEmail: ?string}
     */
    public static function defaults(): array
    {
        $saved = (array) Setting::get(self::SETTING, []);

        return [
            'name' => $saved['name'] ?? self::NAME,
            'colour' => $saved['colour'] ?? self::COLOUR,
            'logo' => $saved['logo'] ?? null,
            'supportEmail' => $saved['supportEmail'] ?? null,
        ];
    }

    /** Saves the default brand. A null value removes the field. */
    public static function saveDefaults(array $brand): void
    {
        $saved = [...(array) Setting::get(self::SETTING, []), ...$brand];
        Setting::put(self::SETTING, array_filter($saved, fn ($v) => $v !== null && $v !== ''));
    }

    /** The reseller whose brand the customer sees, or null for the default brand. */
    public static function resellerOf(?Customer $customer): ?Customer
    {
        if (! $customer) {
            return null;
        }
        if ($customer->role === 'reseller') {
            return $customer;
        }

        return $customer->reseller_id ? $customer->reseller : null;
    }

    /**
     * The brand that a customer sees. Null gives the default brand.
     *
     * @return array<string, mixed>
     */
    public static function forCustomer(?Customer $customer): array
    {
        $reseller = self::resellerOf($customer);
        if (! $reseller) {
            $brand = self::defaults();

            return $brand + [
                'source' => 'panel',
                // The panel shows its built-in logo only when the administrator did not change the brand.
                'builtIn' => $brand['name'] === self::NAME && $brand['logo'] === null,
                'panelDomain' => PanelCertificate::domain(),
                'nameServers' => Zones::nameServers(),
                'mailHost' => null,
            ];
        }
        $own = $reseller->branding ?? [];

        return [
            'name' => $own['name'] ?? $reseller->name,
            'colour' => $own['colour'] ?? self::COLOUR,
            'logo' => $own['logo'] ?? null,
            'supportEmail' => $own['supportEmail'] ?? $reseller->email,
            'source' => 'reseller',
            'builtIn' => false,
            'panelDomain' => $own['panelDomain'] ?? PanelCertificate::domain(),
            'nameServers' => self::nameServersFor($reseller),
            'mailHost' => $own['mailHost'] ?? null,
        ];
    }

    /** @return array<string, mixed> */
    public static function forUser(User $user): array
    {
        // A fresh read: the brand can change in the same request.
        return self::forCustomer($user->customer_id ? Customer::find($user->customer_id) : null);
    }

    /**
     * The brand of the sign-in page. On the panel domain of a reseller, it is the brand of
     * that reseller. Else it is the default brand. It has only the fields that a person who
     * has not signed in can see.
     *
     * @return array{name: string, colour: string, logo: ?string, supportEmail: ?string, builtIn: bool}
     */
    public static function forHost(string $host): array
    {
        $reseller = self::resellerForHost($host);
        $brand = $reseller ? self::forCustomer($reseller) : self::forCustomer(null);

        return array_intersect_key($brand, array_flip(['name', 'colour', 'logo', 'supportEmail', 'builtIn']));
    }

    /** The reseller that has this panel domain, or null for the panel. */
    public static function resellerForHost(string $host): ?Customer
    {
        $host = strtolower(rtrim($host, '.'));

        return $host === '' ? null : Customer::where('role', 'reseller')->whereNotNull('branding')->get()
            ->first(fn (Customer $c) => ($c->branding['panelDomain'] ?? null) === $host);
    }

    /** The URL of the panel that the customer uses: the panel domain of its reseller, if any. */
    public static function panelUrl(?Customer $customer): string
    {
        return 'https://'.self::forCustomer($customer)['panelDomain'];
    }

    /**
     * The host names of the name servers that a customer of the reseller sees. The reseller
     * gives one name for each name server of the panel, in the order of the host names of the
     * panel. When the number does not agree (the administrator added or removed a DNS server),
     * the customers see the host names of the panel until the reseller changes its list.
     *
     * @return list<string>
     */
    public static function nameServersFor(?Customer $customer): array
    {
        $panel = Zones::nameServers();
        $own = self::resellerOf($customer)?->branding['nameServers'] ?? [];

        return $own !== [] && count($own) === count($panel) ? array_values($own) : $panel;
    }

    /**
     * The name that a customer sees for one name server of the panel.
     */
    public static function nameServerName(?Customer $customer, string $panelName): string
    {
        $panel = Zones::nameServers();
        $index = array_search($panelName, $panel, true);
        $own = self::nameServersFor($customer);

        return $index === false ? $panelName : ($own[$index] ?? $panelName);
    }

    /**
     * Checks a colour. White text on the colour must have a contrast ratio of 4.5:1 or more
     * (WCAG 2.2 AA), because the buttons of the panel have white text.
     */
    public static function contrastWithWhite(string $hex): float
    {
        $channel = function (string $h): float {
            $c = hexdec($h) / 255;

            return $c <= 0.04045 ? $c / 12.92 : (($c + 0.055) / 1.055) ** 2.4;
        };
        $hex = ltrim($hex, '#');
        $l = 0.2126 * $channel(substr($hex, 0, 2)) + 0.7152 * $channel(substr($hex, 2, 2)) + 0.0722 * $channel(substr($hex, 4, 2));

        return 1.05 / ($l + 0.05);
    }

    /**
     * The validation rules of the fields of a brand. $prefix is the name of the field that
     * holds the brand, for example "branding.".
     *
     * @return array<string, list<mixed>>
     */
    public static function rules(string $prefix = ''): array
    {
        return [
            $prefix.'name' => ['sometimes', 'nullable', 'string', 'max:100'],
            $prefix.'colour' => ['sometimes', 'nullable', 'regex:/^#[0-9a-fA-F]{6}$/', function (string $attribute, mixed $value, \Closure $fail) {
                if (is_string($value) && self::contrastWithWhite($value) < 4.5) {
                    $fail('Select a darker colour. White text on this colour is hard to read (contrast '.number_format(self::contrastWithWhite($value), 1).':1, at least 4.5:1).');
                }
            }],
            // A data URL, for example data:image/png;base64,iVBORw0...
            $prefix.'logo' => ['sometimes', 'nullable', 'string', 'max:'.(self::LOGO_BYTES * 4 / 3 + 100)],
            $prefix.'supportEmail' => ['sometimes', 'nullable', 'email', 'max:200'],
        ];
    }

    /**
     * Checks a logo and gives it again as a data URL. The panel checks the real type of the
     * image and its size: the file name and the type that the client sends do not count.
     *
     * @throws ValidationException
     */
    public static function logo(string $dataUrl, string $field): string
    {
        if (! preg_match('#^data:(image/[a-z]+);base64,([A-Za-z0-9+/=\s]+)$#', $dataUrl, $m)) {
            throw ValidationException::withMessages([$field => 'Upload a PNG, JPEG or WebP image.']);
        }
        $bytes = base64_decode(preg_replace('/\s+/', '', $m[2]), true);
        if ($bytes === false || $bytes === '') {
            throw ValidationException::withMessages([$field => 'Upload a PNG, JPEG or WebP image.']);
        }
        if (strlen($bytes) > self::LOGO_BYTES) {
            throw ValidationException::withMessages([$field => 'Upload an image of 256 KB or less.']);
        }
        $info = @getimagesizefromstring($bytes);
        if ($info === false || ! in_array($info['mime'], self::LOGO_TYPES, true)) {
            throw ValidationException::withMessages([$field => 'Upload a PNG, JPEG or WebP image.']);
        }
        if ($info[0] > self::LOGO_PIXELS || $info[1] > self::LOGO_PIXELS) {
            throw ValidationException::withMessages([$field => 'Upload an image of 2048 by 2048 pixels or less.']);
        }

        return "data:{$info['mime']};base64,".base64_encode($bytes);
    }

    /**
     * The state of the TLS certificate of each reseller panel domain.
     *
     * @return array<string, array{domain: string, method: string, state: string, expiresAt: ?string, error: ?string, checkedAt: ?string}>
     */
    public static function certificates(): array
    {
        return (array) Setting::get(self::CERTIFICATES, []);
    }

    /** The state of the certificate of one reseller panel domain, or null with no domain. */
    public static function certificateOf(?string $domain): ?array
    {
        if ($domain === null) {
            return null;
        }

        return self::certificates()[$domain] ?? ['domain' => $domain, 'method' => PanelCertificate::publicName($domain) ? 'letsencrypt' : 'internal', 'state' => 'pending', 'expiresAt' => null, 'error' => null, 'checkedAt' => null];
    }

    /**
     * Keeps the states that the agent on the panel server reported.
     *
     * @param  list<array<string, mixed>>  $states
     */
    public static function recordCertificates(array $states): bool
    {
        $domains = self::panelDomains();
        $saved = array_intersect_key(self::certificates(), array_flip($domains));
        foreach ($states as $s) {
            $domain = is_array($s) ? ($s['domain'] ?? null) : null;
            if (! is_string($domain) || ! in_array($domain, $domains, true)) {
                continue;
            }
            $saved[$domain] = [
                'domain' => $domain,
                'method' => in_array($s['method'] ?? null, PanelCertificate::METHODS, true) ? $s['method'] : 'letsencrypt',
                'state' => in_array($s['state'] ?? null, ['valid', 'pending', 'expired'], true) ? $s['state'] : 'pending',
                'expiresAt' => is_string($s['expiresAt'] ?? null) ? $s['expiresAt'] : null,
                'error' => is_string($s['error'] ?? null) ? mb_substr($s['error'], 0, 500) : null,
                'checkedAt' => now()->toIso8601String(),
            ];
        }
        Setting::put(self::CERTIFICATES, $saved);

        return true;
    }

    /**
     * The panel domains of all resellers, in order.
     *
     * @return list<string>
     */
    public static function panelDomains(): array
    {
        return Customer::where('role', 'reseller')->whereNotNull('branding')->orderBy('id')->get(['branding'])
            ->map(fn (Customer $c) => $c->branding['panelDomain'] ?? null)
            ->filter()->unique()->values()->all();
    }

    /**
     * Sends the full list of the reseller panel domains to the agent on the panel server. The
     * agent sets the route and gets the certificates. It does nothing when the panel server has
     * not joined.
     */
    public static function syncPanelDomains(JobDispatcher $jobs): void
    {
        $server = Server::where('local', true)->first();
        if (! $server) {
            return;
        }
        $domains = self::panelDomains();
        $count = count($domains);
        $jobs->dispatch($server, AgentAction::SetPanelBrands, ['domains' => $domains],
            $count === 0 ? 'Remove the reseller panel domains' : ($count === 1 ? "Set the reseller panel domain {$domains[0]}" : "Set {$count} reseller panel domains"));
    }
}
