<?php

namespace Tests\Feature;

use App\Enums\JobState;
use App\Models\AgentJob;
use App\Services\JobOutcome;
use App\Services\Totp;
use Illuminate\Support\Facades\DB;
use Tests\TestCase;

/**
 * Each secret that the panel keeps is encrypted or hashed in the database (docs/security.md).
 * The test makes each kind of secret, then reads each row of each table and looks for the
 * secret as plain text.
 */
class SecretsAtRestTest extends TestCase
{
    public function test_no_secret_is_in_the_database_as_plain_text(): void
    {
        config(['session.driver' => 'database']);
        $website = $this->website($this->customer(), $this->server(), $this->image());
        $user = $this->user();
        $password = 'Correct-Horse-Battery-77';
        $user->forceFill(['password' => $password])->save();
        $secrets = [$password];

        // A sign-in token, and a TOTP secret.
        $session = $this->postJson('/api/v1/auth/login', ['email' => $user->email, 'password' => $password])->assertOk()->json('token');
        $secrets[] = explode('|', $session, 2)[1];
        $auth = fn () => $this->withToken($session);
        $totp = $auth()->postJson('/api/v1/me/totp/setup')->assertOk()->json('secret');
        $secrets[] = $totp;
        $this->assertSecretsNotInTheDatabase($secrets, 'with a TOTP setup that is not complete');
        $auth()->postJson('/api/v1/me/totp/enable', ['password' => $password, 'code' => Totp::code($totp)])->assertNoContent();

        // An API token.
        $token = $auth()->postJson('/api/v1/tokens', ['name' => 'CI', 'scopes' => ['websites:read'], 'expiresInDays' => 30, 'password' => $password, 'totp' => Totp::code($totp, time() + 30)])
            ->assertCreated()->json('token');
        $secrets[] = explode('|', $token, 2)[1];

        // The credentials of the backup storage.
        $auth()->putJson('/api/v1/backup-policy', [
            'daily' => 7, 'weekly' => 4, 'monthly' => 6, 'time' => '03:00', 'target' => 's3:s3.amazonaws.com/backups',
            'credentials' => ['AWS_ACCESS_KEY_ID' => 'AKIAEXAMPLEKEYID0001', 'AWS_SECRET_ACCESS_KEY' => 'wJalrXUtnFEMI-secret-access-key-0001'],
        ])->assertOk();
        $secrets[] = 'AKIAEXAMPLEKEYID0001';
        $secrets[] = 'wJalrXUtnFEMI-secret-access-key-0001';

        // Database passwords. The jobs that carry them are not finished yet.
        $database = $auth()->postJson("/api/v1/websites/{$website->id}/databases", ['engine' => 'mariadb', 'name' => 'shop'])->assertAccepted();
        $secrets[] = $database->json('password');
        $dbUser = $auth()->postJson("/api/v1/websites/{$website->id}/database-users", ['engine' => 'mariadb', 'name' => 'report'])->assertAccepted();
        $secrets[] = $dbUser->json('password');
        $this->assertSecretsNotInTheDatabase($secrets, 'while the jobs wait for the agent');

        // When the jobs are finished, the panel removes the passwords from the job data.
        foreach ([$database->json('job.id'), $dbUser->json('job.id')] as $id) {
            app(JobOutcome::class)->finish(AgentJob::findOrFail($id), JobState::Succeeded, null, []);
        }
        foreach (AgentJob::all() as $job) {
            $this->assertStringNotContainsString($database->json('password'), json_encode($job->payload));
            $this->assertStringNotContainsString($dbUser->json('password'), json_encode($job->payload));
        }
        $this->assertSecretsNotInTheDatabase($secrets, 'after the jobs');
    }

    /** @param  list<string>  $secrets */
    private function assertSecretsNotInTheDatabase(array $secrets, string $when): void
    {
        $tables = array_column(DB::select("select name from sqlite_master where type = 'table' and name not like 'sqlite_%'"), 'name');
        $this->assertContains('settings', $tables);
        foreach ($tables as $table) {
            // The values only: a column can have the name "password".
            $rows = json_encode(DB::table($table)->get()->map(fn ($row) => array_values((array) $row)));
            foreach ($secrets as $secret) {
                $this->assertNotEmpty($secret);
                $this->assertStringNotContainsString($secret, $rows, "The table {$table} has a secret as plain text {$when}.");
            }
        }
    }
}
