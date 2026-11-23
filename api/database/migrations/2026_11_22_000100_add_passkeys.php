<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * FR-103: passkeys (WebAuthn). A user signs in with a passkey in place of the password and the
 * TOTP code. Each passkey is for one panel domain (the relying party ID).
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('passkeys', function (Blueprint $table) {
            $table->ulid('id')->primary();
            $table->foreignId('user_id')->constrained()->cascadeOnDelete();
            $table->string('name', 100);
            // The panel domain for which the browser made the passkey.
            $table->string('rp_id', 253);
            // The SHA-256 hash of the credential ID, in hexadecimal: the key to find a passkey.
            $table->string('credential_hash', 64)->unique();
            // The WebAuthn user handle (base64url). All passkeys of a user have the same handle.
            $table->string('user_handle', 100)->index();
            // The credential record of the WebAuthn library, in JSON: the public key, the
            // counter and the flags. It has no secret.
            $table->text('record');
            $table->boolean('backup_eligible')->default(false);
            $table->boolean('backed_up')->default(false);
            $table->string('aaguid', 36)->nullable();
            $table->timestamp('last_used_at')->nullable();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('passkeys');
    }
};
