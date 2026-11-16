<?php

use Illuminate\Database\Migrations\Migration;
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Schema;

/**
 * Adversarial review, findings A1-02 and A1-10.
 *
 * - The panel keeps each email of a user in lower case. Thus two accounts cannot have emails
 *   that are different only in the case of their letters, and the sign-in lock of an account
 *   is for one account only.
 * - temporary_password_at is the time when the user got a temporary password. A temporary
 *   password stops after 72 hours.
 */
return new class extends Migration
{
    public function up(): void
    {
        Schema::table('users', function (Blueprint $table) {
            $table->timestamp('temporary_password_at')->nullable();
        });
        $taken = [];
        foreach (DB::table('users')->orderBy('id')->get(['id', 'email']) as $user) {
            $taken[$user->email] = true;
        }
        foreach (DB::table('users')->orderBy('id')->get(['id', 'email']) as $user) {
            $lower = mb_strtolower(trim($user->email));
            // An installation that has two such accounts keeps them: an administrator can then
            // delete one of them.
            if ($lower === $user->email || isset($taken[$lower])) {
                continue;
            }
            DB::table('users')->where('id', $user->id)->update(['email' => $lower]);
            $taken[$lower] = true;
        }
    }

    public function down(): void
    {
        Schema::table('users', function (Blueprint $table) {
            $table->dropColumn('temporary_password_at');
        });
    }
};
