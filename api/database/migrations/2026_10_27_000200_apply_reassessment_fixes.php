<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Changes for the findings of the security reassessment of release 0.1 (R-01 to R-17).
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('customers', function (Blueprint $table) {
            // R-06: "administrator" or "reseller". Only an administrator lifts the suspension
            // of an administrator.
            $table->string('suspended_by')->nullable();
            // R-07: the plans that a reseller can give to its customers. Null means only the
            // plan of the reseller.
            $table->json('plan_ids')->nullable();
        });
    }

    public function down(): void
    {
        Schema::table('customers', fn (Blueprint $table) => $table->dropColumn(['suspended_by', 'plan_ids']));
    }
};
