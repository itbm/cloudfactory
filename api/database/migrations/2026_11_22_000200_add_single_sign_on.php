<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Single sign-on: an administrator adds OpenID Connect and SAML identity providers. A user
 * connects an account of a provider, and then signs in with that provider.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('identity_providers', function (Blueprint $table) {
            $table->ulid('id')->primary();
            // The text of the button: "Sign in with <name>".
            $table->string('name', 100);
            // "oidc" or "saml".
            $table->string('protocol', 10);
            $table->boolean('enabled')->default(true);
            // OpenID Connect: the issuer, the client ID and the client secret (encrypted).
            $table->string('issuer', 500)->nullable();
            $table->string('client_id', 255)->nullable();
            $table->text('client_secret')->nullable();
            // SAML: the entity ID, the address for the sign-in and the certificate of the provider.
            $table->string('entity_id', 500)->nullable();
            $table->string('sso_url', 500)->nullable();
            $table->text('certificate')->nullable();
            // The first sign-in connects the user with the same email, when the email domain
            // is in the list. Else each user connects the account in Settings.
            $table->boolean('link_by_email')->default(false);
            $table->json('email_domains');
            $table->timestamps();
        });

        Schema::create('user_identities', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignId('user_id')->constrained()->cascadeOnDelete();
            $table->foreignUlid('identity_provider_id')->constrained()->cascadeOnDelete();
            // The ID of the account at the provider: the "sub" claim, or the SAML NameID.
            $table->string('subject', 255);
            // The email that the provider gave at the last sign-in, to show to the user.
            $table->string('email', 255)->nullable();
            $table->timestamp('last_used_at')->nullable();
            $table->timestamps();
            $table->unique(['identity_provider_id', 'subject']);
            $table->unique(['user_id', 'identity_provider_id']);
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('user_identities');
        Schema::dropIfExists('identity_providers');
    }
};
