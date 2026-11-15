<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Encryption at rest for a mailbox. The owner gives an OpenPGP public key, and the mail server
 * encrypts each new message with it before it stores the message.
 *
 * - "public_key" is the key block that the mail server has. A public key is not a secret.
 * - "encryption" is what the mail server found in the key: its fingerprints, its type, the date
 *   when it expires and warnings. Null: the encryption is off.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('mailboxes', function (Blueprint $table) {
            $table->text('public_key')->nullable();
            $table->json('encryption')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('mailboxes', fn (Blueprint $table) => $table->dropColumn(['public_key', 'encryption']));
    }
};
