<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Named networks. A customer can make networks in addition to the network "default", and
 * select the websites and the database servers of each one. The plan gives the number of
 * these networks. 0 turns the function off: the customer has only "default".
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('plans', function (Blueprint $table) {
            $table->unsignedInteger('networks')->default(0);
        });
    }

    public function down(): void
    {
        Schema::table('plans', fn (Blueprint $table) => $table->dropColumn('networks'));
    }
};
