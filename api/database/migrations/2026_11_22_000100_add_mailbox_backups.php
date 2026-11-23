<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The backups of the mailboxes. Each row is one restic snapshot of one mailbox, in the
 * repository of the server of the mail domain ("server_id").
 *
 * A row stays after its mailbox is removed: "remove_after" is then set. The weekly job
 * removes the snapshots of the mailbox from the repository after that time, and then the row.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('mailbox_snapshots', function (Blueprint $table) {
            $table->ulid('id')->primary();
            // The mailbox can be removed before the snapshot, so there is no foreign key.
            $table->string('mailbox_id', 26)->index();
            $table->string('address');
            $table->string('server_id', 26)->index();
            $table->string('kind', 16);
            $table->string('restic_id', 64);
            $table->unsignedBigInteger('size_bytes')->default(0);
            $table->unsignedInteger('messages')->default(0);
            // The result of the last restore of this backup: the time and the number of
            // messages that the restore added, found in the mailbox, or could not add.
            $table->json('last_restore')->nullable();
            $table->timestamp('remove_after')->nullable()->index();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('mailbox_snapshots');
    }
};
