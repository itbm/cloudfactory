<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

/**
 * Audit finding F05: a suspension turns off the SFTP access of each website of the customer.
 * This column records the websites that had SFTP, so that they get it again when the
 * suspension ends.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('websites', function (Blueprint $table) {
            $table->boolean('sftp_closed_by_suspension')->default(false);
        });
    }

    public function down(): void
    {
        Schema::table('websites', fn (Blueprint $table) => $table->dropColumn('sftp_closed_by_suspension'));
    }
};
