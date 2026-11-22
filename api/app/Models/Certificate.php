<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsTo;

/**
 * A TLS certificate of a customer that the panel keeps (docs/certificates.md).
 *
 * - "upload": the customer uploaded the certificate chain and its private key.
 * - "acme": the panel gets the certificate from an ACME CA, for example Let's Encrypt, with
 *   DNS validation (DNS-01) in a DNS zone of the customer. This is the only way to get a
 *   wildcard certificate. The panel renews the certificate before it expires.
 *
 * A website domain of the customer uses the best certificate of the customer that is valid
 * for it (Certificates::forHost). Other domains get a certificate from the edge proxy.
 */
#[Fillable([
    'customer_id', 'source', 'names', 'certificate', 'private_key', 'fingerprint', 'issuer', 'not_before', 'expires_at', 'trusted',
    'order', 'order_key', 'error', 'next_try_at', 'tries', 'renew_at', 'user_id',
])]
#[Hidden(['private_key', 'order_key'])]
class Certificate extends Model
{
    use HasUlids;

    public const UPLOAD = 'upload';

    public const ACME = 'acme';

    protected function casts(): array
    {
        return [
            'names' => 'array',
            // The private keys never leave the panel, except to the agent of a server.
            'private_key' => 'encrypted',
            'order_key' => 'encrypted',
            'order' => 'array',
            'trusted' => 'boolean',
            'not_before' => 'datetime',
            'expires_at' => 'datetime',
            'next_try_at' => 'datetime',
            'renew_at' => 'datetime',
            'tries' => 'integer',
        ];
    }

    public function customer(): BelongsTo
    {
        return $this->belongsTo(Customer::class);
    }

    /** True when the certificate can serve a host now: it has a chain, and it did not expire. */
    public function usable(): bool
    {
        return $this->certificate !== null && $this->private_key !== null
            && $this->expires_at !== null && $this->expires_at->isFuture()
            && ($this->not_before === null || $this->not_before->isPast());
    }

    /**
     * True when the certificate is valid for a host: a name of the certificate is the host, or
     * a wildcard name agrees with the host. A wildcard agrees with one label only, as in a
     * browser: "*.example.com" is valid for "www.example.com", not for "example.com" or
     * "a.b.example.com".
     */
    public function covers(string $host): bool
    {
        return self::namesCover($this->names ?? [], $host);
    }

    /** @param  list<string>  $names */
    public static function namesCover(array $names, string $host): bool
    {
        $host = strtolower($host);
        foreach ($names as $name) {
            if ($name === $host) {
                return true;
            }
            if (str_starts_with($name, '*.')) {
                $dot = strpos($host, '.');
                if ($dot !== false && $dot > 0 && substr($host, $dot + 1) === substr($name, 2)) {
                    return true;
                }
            }
        }

        return false;
    }

    /** True when the host is a name of the certificate, not only a match of a wildcard. */
    public function hasName(string $host): bool
    {
        return in_array(strtolower($host), $this->names ?? [], true);
    }

    /** True while an ACME order of the certificate runs. */
    public function ordering(): bool
    {
        return $this->order !== null;
    }

    /**
     * The state for a user: "issuing" (the first order runs), "failed" (the first order
     * failed), "renewing" (an order for a new certificate runs), "expired" or "valid".
     */
    public function state(): string
    {
        if ($this->certificate === null) {
            return $this->ordering() ? 'issuing' : 'failed';
        }
        if ($this->expires_at === null || $this->expires_at->isPast()) {
            return $this->ordering() ? 'renewing' : 'expired';
        }

        return $this->ordering() ? 'renewing' : 'valid';
    }
}
