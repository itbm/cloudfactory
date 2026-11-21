<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The usual start command and install command of an application image, for example of the
 * runtime of a pack (docs/packs.md). The New website form fills its fields with them.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('images', function (Blueprint $table) {
            $table->string('default_command', 1000)->nullable();
            $table->string('default_install', 1000)->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('images', function (Blueprint $table) {
            $table->dropColumn(['default_command', 'default_install']);
        });
    }
};
