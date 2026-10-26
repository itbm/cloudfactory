<?php

namespace Tests\Unit;

use App\Services\Totp;
use PHPUnit\Framework\TestCase;

class TotpTest extends TestCase
{
    /** The test vector from RFC 6238, appendix B (SHA-1, 8 digits, cut to 6). */
    public function test_rfc_6238_vector(): void
    {
        $secret = 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ'; // "12345678901234567890" in base32

        $this->assertSame('287082', Totp::code($secret, 59));
        $this->assertSame('081804', Totp::code($secret, 1111111109));
    }

    public function test_a_code_from_the_previous_period_is_accepted(): void
    {
        $secret = Totp::generateSecret();

        $this->assertTrue(Totp::verify($secret, Totp::code($secret, 1000), 1030));
        $this->assertFalse(Totp::verify($secret, Totp::code($secret, 1000), 1100));
        $this->assertFalse(Totp::verify($secret, 'abcdef', 1000));
    }
}
