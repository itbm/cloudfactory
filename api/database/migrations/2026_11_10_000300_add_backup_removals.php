<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Audit finding R8: the backups of a deleted website stay in the repository for a set time.
 * After that time, the weekly job removes them. This table has one row for each deleted
 * website until its backups are gone.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::create('backup_removals', function (Blueprint $table) {
            $table->id();
            // The website has no row in "websites" now, so there is no foreign key.
            $table->string('website_id', 26)->unique();
            $table->string('website_name');
            $table->timestamp('remove_after')->index();
            $table->timestamps();
        });
    }

    public function down(): void
    {
        Schema::dropIfExists('backup_removals');
    }
};
