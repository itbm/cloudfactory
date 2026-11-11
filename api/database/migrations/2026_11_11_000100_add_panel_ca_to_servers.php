<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The panel can have a certificate from its own CA. Each agent reports the fingerprint of the
 * root certificate of the panel CA that it trusts. The panel changes to its own CA only when
 * all other servers trust it. Null means an agent that does not trust the panel CA, for
 * example an agent from an earlier release.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('servers', function (Blueprint $table) {
            $table->string('panel_ca_sha256', 64)->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('servers', fn (Blueprint $table) => $table->dropColumn('panel_ca_sha256'));
    }
};
