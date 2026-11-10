<?php

namespace App\Console\Commands;

use App\Services\Audit;
use App\Services\LoginThrottle;
use Illuminate\Console\Attributes\Description;
use Illuminate\Console\Attributes\Signature;
use Illuminate\Console\Command;

/**
 * Audit finding S3: removes the sign-in lock of an account after many failed attempts
 * ("cf-agent panel unlock").
 */
#[Signature('cloudfactory:unlock {email : The email address of the account} {--address= : Also remove the lock for this client address}')]
#[Description('Remove the sign-in lock of an account.')]
class UnlockSignIn extends Command
{
    public function handle(): int
    {
        $email = (string) $this->argument('email');
        $address = $this->option('address');
        if ($address !== null && filter_var($address, FILTER_VALIDATE_IP) === false) {
            $this->error('Give a valid IP address.');

            return self::FAILURE;
        }
        LoginThrottle::unlock($email, $address);
        Audit::system('Removed the sign-in lock on the panel server', $email);
        $this->line("The sign-in lock for {$email} is removed.");

        return self::SUCCESS;
    }
}
