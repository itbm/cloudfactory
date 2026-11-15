<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The OpenPGP keys that the panel makes for a mailbox.
 *
 * - "private_key" has the private keys of the mailbox in ASCII armour, the newest first. Each
 *   key is protected with the password that the mailbox had when the panel made the key. The
 *   panel also encrypts the column with its own key. Null: the panel made no key.
 * - "key_password_current" is false when the mailbox got a new password and the private keys
 *   kept an old password.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('mailboxes', function (Blueprint $table) {
            $table->longText('private_key')->nullable();
            $table->boolean('key_password_current')->default(true);
        });
    }

    public function down(): void
    {
        Schema::table('mailboxes', fn (Blueprint $table) => $table->dropColumn(['private_key', 'key_password_current']));
    }
};
