<?php

namespace App\Console\Commands;

use App\Http\Controllers\Api\UserController;
use App\Models\User;
use App\Services\Audit;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;

/**
 * Audit finding B5: the way back for an administrator who lost the password or the TOTP
 * device. Only a person with root access to the panel server can run this command
 * ("cf-agent panel reset-password").
 */
#[Signature('cloudfactory:reset-password {email : The email address of the user} {--remove-totp : Also remove TOTP, for a user who lost the device}')]
#[Description('Give a user a new temporary password, and sign the user out.')]
class ResetPassword extends Command
{
    public function handle(): int
    {
        $user = User::where('email', (string) $this->argument('email'))->first();
        if (! $user) {
            $this->error('No user has this email address.');

            return self::FAILURE;
        }
        $removeTotp = (bool) $this->option('remove-totp');
        $password = UserController::reset($user, $removeTotp);
        Audit::system($removeTotp ? 'Reset password and removed TOTP on the panel server' : 'Reset password on the panel server', $user->email);

        $this->line("Temporary password for {$user->email}: {$password}");
        $this->line('The user must set a new password at the next sign-in.');
        if ($user->totp_secret) {
            $this->line('TOTP is still on for this user. Use --remove-totp if the user lost the device.');
        }

        return self::SUCCESS;
    }
}
