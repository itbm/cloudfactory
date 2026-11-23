<?php

namespace App\Services\Sso;

/** The account at an identity provider that a sign-in showed. */
final readonly class Account
{
    public function __construct(
        /** The ID of the account at the provider: the "sub" claim, or the SAML NameID. */
        public string $subject,
        public ?string $email,
        /** The provider says that the person controls the email. */
        public bool $emailVerified,
        public ?string $name = null,
    ) {}
}
