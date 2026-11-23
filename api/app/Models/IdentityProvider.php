<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Attributes\Fillable;
use Illuminate\Database\Eloquent\Attributes\Hidden;
use Illuminate\Database\Eloquent\Concerns\HasUlids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\HasMany;

/** Single sign-on: an OpenID Connect or SAML identity provider that an administrator added. */
#[Fillable(['name', 'protocol', 'enabled', 'issuer', 'client_id', 'client_secret', 'entity_id', 'sso_url', 'certificate', 'link_by_email', 'email_domains'])]
#[Hidden(['client_secret'])]
class IdentityProvider extends Model
{
    use HasUlids;

    public const OIDC = 'oidc';

    public const SAML = 'saml';

    protected function casts(): array
    {
        return [
            'enabled' => 'boolean',
            'client_secret' => 'encrypted',
            'link_by_email' => 'boolean',
            'email_domains' => 'array',
        ];
    }

    public function identities(): HasMany
    {
        return $this->hasMany(UserIdentity::class);
    }

    /** The email domain is in the list of the provider. An empty list accepts all domains. */
    public function acceptsEmail(?string $email): bool
    {
        if ($email === null || ! str_contains($email, '@')) {
            return false;
        }
        $domain = strtolower(substr($email, strrpos($email, '@') + 1));

        return $this->email_domains === [] || in_array($domain, $this->email_domains, true);
    }
}
