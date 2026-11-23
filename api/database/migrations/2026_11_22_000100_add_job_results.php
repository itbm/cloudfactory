<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * The result of a job that the user must see, for example the summary of an archive of the
 * file manager (AgentAction::keepsResult).
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('agent_jobs', function (Blueprint $table) {
            $table->json('result')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('agent_jobs', fn (Blueprint $table) => $table->dropColumn('result'));
    }
};
