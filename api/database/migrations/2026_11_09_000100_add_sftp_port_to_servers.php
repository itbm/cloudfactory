<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Spec section 10: the SFTP container of the websites has its own port on each server
 * (2200 by default). The agent reports the port. Null means an agent of release 0.1, which
 * uses the SSH service of the host on port 22.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('servers', function (Blueprint $table) {
            $table->unsignedInteger('sftp_port')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn('sftp_port'));
    }
};
