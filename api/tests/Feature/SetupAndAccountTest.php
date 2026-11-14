<?php

namespace Tests\Feature;

use App\Enums\Role;
use App\Models\Database;
use App\Models\Setting;
use App\Services\PanelCertificate;
use Illuminate\Support\Facades\Crypt;
use Tests\TestCase;

/** The setup checklist of the panel, and the account of the signed-in user. */
class SetupAndAccountTest extends TestCase
{
    /** @return array<string, array<string, mixed>> the items of the checklist by ID */
    private function items(): array
    {
        return collect($this->getJson('/api/v1/setup')->assertOk()->json('items'))->keyBy('id')->all();
    }

    public function test_a_new_panel_has_open_items_with_their_effect(): void
    {
        config(['cloudfactory.panel_domain' => 'panel.example.com']);
        $admin = $this->actingWith($this->user());
        $this->server(['name' => 'web-1', 'services' => [['name' => 'disk-quotas', 'state' => 'stopped', 'optional' => true]]]);
        $this->server(['name' => 'web-2', 'services' => [['name' => 'disk-quotas', 'state' => 'running', 'optional' => true]]]);
        PanelCertificate::record(['domain' => 'panel.example.com', 'method' => 'internal', 'state' => 'valid']);

        $this->getJson('/api/v1/setup')->assertOk()->assertJson(['complete' => false, 'open' => 4]);
        $items = $this->items();
        $this->assertSame(['backups', 'totp', 'disk-limits', 'panel-certificate'], array_keys($items));
        foreach ($items as $item) {
            $this->assertSame('open', $item['state']);
            $this->assertNotSame('', $item['effect']);
        }
        $this->assertSame('No object storage is set.', $items['backups']['detail']);
        $this->assertStringContainsString($admin->email, $items['totp']['detail']);
        $this->assertSame('Turn on two-step sign-in', $items['totp']['action']['label']);
        $this->assertStringContainsString('web-1', $items['disk-limits']['detail']);
        $this->assertStringNotContainsString('web-2', $items['disk-limits']['detail']);
        $this->assertStringStartsWith('/servers/', $items['disk-limits']['action']['target']);
    }

    public function test_the_items_are_done_when_the_panel_is_set_up(): void
    {
        config(['cloudfactory.panel_domain' => 'panel.example.com']);
        $admin = $this->user();
        $admin->forceFill(['totp_secret' => 'JBSWY3DPEHPK3PXP'])->save();
        $this->actingWith($admin);
        $this->server(['services' => [['name' => 'disk-quotas', 'state' => 'running', 'optional' => true]]]);
        Setting::put('backup_policy', ['daily' => 7, 'weekly' => 4, 'monthly' => 6, 'time' => '02:30', 'target' => 's3:s3.example.com/backups']);
        Setting::put('backup_credentials', Crypt::encryptString(json_encode(['AWS_ACCESS_KEY_ID' => 'a', 'AWS_SECRET_ACCESS_KEY' => 'b'])));
        PanelCertificate::record(['domain' => 'panel.example.com', 'method' => 'letsencrypt', 'state' => 'valid']);

        $this->getJson('/api/v1/setup')->assertOk()->assertJson(['complete' => true, 'open' => 0]);
        $this->assertSame(['done', 'done', 'done', 'done'], array_column($this->items(), 'state'));
    }

    public function test_an_item_that_does_not_apply_is_not_open(): void
    {
        // Let's Encrypt gives no certificate for panel.localhost, and no server has joined.
        config(['cloudfactory.panel_domain' => 'panel.localhost']);
        $this->actingWith($this->user());
        PanelCertificate::record(['domain' => 'panel.localhost', 'method' => 'internal', 'state' => 'valid']);

        $items = $this->items();
        $this->assertSame('not_applicable', $items['panel-certificate']['state']);
        $this->assertSame('not_applicable', $items['disk-limits']['state']);
        // Object storage without credentials is open.
        Setting::put('backup_policy', ['target' => 's3:s3.example.com/backups']);
        $this->assertStringContainsString('no credentials', $this->items()['backups']['detail']);
    }

    public function test_only_an_administrator_reads_the_checklist(): void
    {
        $customer = $this->customer();
        $this->actingWith($this->user(Role::Owner, $customer));
        $this->getJson('/api/v1/setup')->assertForbidden();
    }

    public function test_an_owner_reads_the_account_with_the_plan_the_usage_and_the_provider(): void
    {
        $plan = $this->plan();
        $reseller = $this->customer($plan, ['name' => 'Northwind Agency', 'email' => 'hello@northwind.example', 'role' => 'reseller']);
        $customer = $this->customer($plan, ['name' => 'Corner Bakery', 'email' => 'bea@cornerbakery.example', 'reseller_id' => $reseller->id]);
        $other = $this->customer($plan, ['name' => 'Harbour Books', 'email' => 'hari@harbourbooks.example', 'reseller_id' => $reseller->id]);
        $server = $this->server();
        $image = $this->image();
        $website = $this->website($customer, $server, $image, 'cornerbakery.example', ['usage' => ['diskBytes' => 1000]]);
        $this->website($customer, $server, $image, 'orders.cornerbakery.example', ['usage' => ['diskBytes' => 500]]);
        $this->website($other, $server, $image, 'harbourbooks.example', ['usage' => ['diskBytes' => 9000]]);
        Database::create(['website_id' => $website->id, 'engine' => 'mariadb', 'name' => 'bakery']);
        $user = $this->actingWith($this->user(Role::Owner, $customer));
        $this->user(Role::Owner, $other);

        $this->getJson('/api/v1/account')->assertOk()
            ->assertJsonPath('customer.name', 'Corner Bakery')
            ->assertJsonPath('customer.suspended', false)
            ->assertJsonPath('backups.available', false)
            ->assertJsonPath('provider.name', 'Northwind Agency')
            ->assertJsonPath('plan.name', 'Business')
            ->assertJsonPath('usage', ['websites' => 2, 'databases' => 1, 'diskBytes' => 1500, 'customers' => null])
            ->assertJsonCount(1, 'users')
            ->assertJsonPath('users.0.email', $user->email);

        // The account shows that the panel can make backups, but not where they go.
        Setting::put('backup_policy', ['daily' => 7, 'weekly' => 4, 'monthly' => 6, 'time' => '02:30', 'target' => 's3:s3.example.com/backups']);
        Setting::put('backup_credentials', Crypt::encryptString(json_encode(['AWS_ACCESS_KEY_ID' => 'a', 'AWS_SECRET_ACCESS_KEY' => 'b'])));
        $this->getJson('/api/v1/account')->assertOk()->assertJsonPath('backups', ['available' => true]);

        // A reseller sees its own account, with the number of its customers.
        $this->app['auth']->forgetGuards();
        $this->actingWith($this->user(Role::Reseller, $reseller));
        $this->getJson('/api/v1/account')->assertOk()
            ->assertJsonPath('customer.name', 'Northwind Agency')
            ->assertJsonPath('provider', null)
            ->assertJsonPath('usage.websites', 0)
            ->assertJsonPath('usage.customers', 2);
    }

    public function test_an_administrator_has_no_account(): void
    {
        $this->actingWith($this->user());
        $this->getJson('/api/v1/account')->assertOk()->assertExactJson(['customer' => null, 'provider' => null, 'plan' => null, 'usage' => null, 'backups' => ['available' => false], 'users' => []]);
    }
}
