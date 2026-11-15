<?php

namespace Tests\Feature;

use App\Enums\Role;
use App\Events\UsageAlertRaised;
use App\Events\UsageAlertResolved;
use App\Models\Server;
use App\Models\UsageAlert;
use App\Models\Website;
use App\Services\UsageRecorder;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Event;
use Tests\TestCase;

/** Usage history and usage alerts. */
class UsageTest extends TestCase
{
    private const GB = 1024 ** 3;

    private Server $srv;

    private Website $site;

    protected function setUp(): void
    {
        parent::setUp();
        $this->srv = $this->server(['agent_token_hash' => hash('sha256', 'cfa_secret'), 'certificate_fingerprint' => self::LOCAL_FINGERPRINT, 'local' => true]);
        $plan = $this->plan(['limits' => ['vcpus' => 2, 'memoryBytes' => 2 * self::GB, 'diskBytes' => 10 * self::GB, 'diskIoBytesPerSecond' => 1, 'processes' => 100, 'bandwidthBytes' => 100 * self::GB]]);
        $this->site = $this->website($this->customer($plan), $this->srv, $this->image());
    }

    private function report(array $website = [], array $server = []): void
    {
        $this->asLocalAgent('cfa_secret')->postJson('/api/agent/v1/report', [
            'agentVersion' => '0.1.0',
            'usage' => $server + ['cpuPercent' => 10, 'memoryPercent' => 20, 'diskPercent' => 30, 'networkInBytes' => 1000, 'networkOutBytes' => 2000],
            'websites' => [$website + ['id' => $this->site->id, 'cpuPercent' => 50, 'memoryBytes' => self::GB, 'diskBytes' => self::GB, 'processes' => 10, 'bytesIn' => 100, 'bytesOut' => 900]],
        ])->assertOk();
    }

    public function test_each_report_adds_to_the_buckets_of_5_minutes_and_of_1_hour(): void
    {
        $this->travelTo(Carbon::parse('2026-11-15 10:01:00', 'UTC'));
        $this->report();
        $this->travelTo(Carbon::parse('2026-11-15 10:03:00', 'UTC'));
        $this->report(['cpuPercent' => 150, 'memoryBytes' => 3 * self::GB, 'diskBytes' => 2 * self::GB]);
        $this->travelTo(Carbon::parse('2026-11-15 10:06:00', 'UTC'));
        $this->report();

        $fine = DB::table('usage_samples')->where(['subject_type' => 'website', 'resolution' => 5])->orderBy('bucket_at')->get();
        $this->assertCount(2, $fine);
        $this->assertSame('2026-11-15 10:00:00', $fine[0]->bucket_at);
        $this->assertEquals(2, $fine[0]->samples);
        $this->assertEquals(200, $fine[0]->cpu_sum);
        $this->assertEquals(150, $fine[0]->cpu_max);
        $this->assertEquals(3 * self::GB, $fine[0]->memory_max);
        // The disk use is the newest value.
        $this->assertEquals(2 * self::GB, $fine[0]->disk);
        $this->assertEquals(200, $fine[0]->bytes_in);
        $coarse = DB::table('usage_samples')->where(['subject_type' => 'website', 'resolution' => 60])->sole();
        $this->assertEquals(3, $coarse->samples);
        $this->assertEquals(3000, $coarse->bytes_out + $coarse->bytes_in);
        $this->assertSame(1, DB::table('usage_samples')->where(['subject_type' => 'server', 'resolution' => 60])->count());
        // The traffic of the month.
        $this->assertSame(3000, (int) $this->site->fresh()->bandwidth_bytes);

        // A new month starts again.
        $this->travelTo(Carbon::parse('2026-12-01 00:01:00', 'UTC'));
        $this->report();
        $this->assertSame(1000, (int) $this->site->fresh()->bandwidth_bytes);
        $this->assertSame('2026-12', $this->site->fresh()->bandwidth_month);
    }

    public function test_the_graph_has_a_point_for_each_step_and_a_gap_with_no_report(): void
    {
        $this->travelTo(Carbon::parse('2026-11-15 10:01:00', 'UTC'));
        $this->report();
        $this->travelTo(Carbon::parse('2026-11-15 10:12:00', 'UTC'));
        $this->report(['cpuPercent' => 80]);
        $owner = $this->user(Role::Owner, $this->site->customer);
        $this->actingWith($owner);

        $r = $this->getJson("/api/v1/websites/{$this->site->id}/usage?range=24h")->assertOk();
        $points = collect($r->json('points'))->keyBy('at');
        $this->assertSame(5, $r->json('stepMinutes'));
        $this->assertEquals(50, $points['2026-11-15T10:00:00Z']['cpuPercent']);
        $this->assertNull($points['2026-11-15T10:05:00Z']['cpuPercent']);
        $this->assertEquals(80, $points['2026-11-15T10:10:00Z']['cpuPercent']);
        $this->assertEquals(1000, $points['2026-11-15T10:10:00Z']['bytesIn'] + $points['2026-11-15T10:10:00Z']['bytesOut']);
        $this->assertSame(2000, $r->json('bandwidth.bytes'));
        $this->assertSame(100 * self::GB, $r->json('bandwidth.limitBytes'));

        $week = $this->getJson("/api/v1/websites/{$this->site->id}/usage?range=7d")->assertOk();
        $this->assertSame(60, $week->json('stepMinutes'));
        $this->assertEquals(65, collect($week->json('points'))->firstWhere('at', '2026-11-15T10:00:00Z')['cpuPercent']);
        $this->getJson("/api/v1/websites/{$this->site->id}/usage?range=2d")->assertUnprocessable();

        // A different customer does not see the history, and only an administrator sees the
        // history of a server.
        $other = $this->user(Role::Owner, $this->customer());
        $this->actingWith($other);
        $this->getJson("/api/v1/websites/{$this->site->id}/usage")->assertNotFound();
        $this->actingWith($owner);
        $this->getJson("/api/v1/servers/{$this->srv->id}/usage")->assertForbidden();
        $this->actingWith($this->user());
        $server = $this->getJson("/api/v1/servers/{$this->srv->id}/usage")->assertOk();
        $this->assertEquals(30, collect($server->json('points'))->firstWhere('at', '2026-11-15T10:00:00Z')['diskPercent']);
    }

    public function test_an_alert_opens_near_a_limit_goes_critical_and_closes(): void
    {
        Event::fake([UsageAlertRaised::class, UsageAlertResolved::class]);
        $this->travelTo(Carbon::parse('2026-11-15 10:00:00', 'UTC'));
        // 8.5 GB of 10 GB.
        $this->report(['diskBytes' => (int) (8.5 * self::GB)]);
        $alert = UsageAlert::sole();
        $this->assertSame(['website', 'disk', 'warning', 85.0], [$alert->subject_type, $alert->resource, $alert->level, $alert->percent]);
        $this->assertSame($this->site->customer_id, $alert->customer_id);
        Event::assertDispatched(UsageAlertRaised::class, fn ($e) => $e->opened && $e->alert->is($alert));

        // The same level: no second event.
        $this->report(['diskBytes' => (int) (8.6 * self::GB)]);
        Event::assertDispatchedTimes(UsageAlertRaised::class, 1);
        // Critical.
        $this->report(['diskBytes' => (int) (9.6 * self::GB)]);
        $this->assertSame('critical', $alert->fresh()->level);
        Event::assertDispatched(UsageAlertRaised::class, fn ($e) => ! $e->opened && $e->alert->level === 'critical');
        // A small drop keeps it critical, and below the warning threshold it stays open
        // until 5 points below.
        $this->report(['diskBytes' => (int) (9.2 * self::GB)]);
        $this->assertSame('critical', $alert->fresh()->level);
        $this->report(['diskBytes' => (int) (7.8 * self::GB)]);
        $this->assertTrue($alert->fresh()->isOpen());
        $this->assertSame('warning', $alert->fresh()->level);
        $this->report(['diskBytes' => (int) (7.4 * self::GB)]);
        $this->assertFalse($alert->fresh()->isOpen());
        Event::assertDispatched(UsageAlertResolved::class);
    }

    public function test_cpu_and_memory_alerts_use_the_average_of_30_minutes(): void
    {
        $this->travelTo(Carbon::parse('2026-11-15 10:00:00', 'UTC'));
        // A short peak: 4 reports at 2 vCPUs.
        foreach (range(1, 4) as $i) {
            $this->travelTo(now()->addMinute());
            $this->report(['cpuPercent' => 200]);
        }
        $this->assertSame(0, UsageAlert::where('resource', 'cpu')->count());
        $this->travelTo(now()->addMinute());
        $this->report(['cpuPercent' => 200]);
        $alert = UsageAlert::where('resource', 'cpu')->sole();
        $this->assertSame('critical', $alert->level);
        $this->assertSame('2 vCPUs', UsageRecorder::amount($alert, $alert->used));
    }

    public function test_bandwidth_alert_and_server_alerts_and_who_sees_them(): void
    {
        $this->travelTo(Carbon::parse('2026-11-15 10:00:00', 'UTC'));
        $this->site->update(['bandwidth_month' => '2026-11', 'bandwidth_bytes' => 90 * self::GB]);
        $this->report([], ['diskPercent' => 97]);
        $this->assertSame('warning', UsageAlert::where('resource', 'bandwidth')->sole()->level);
        $serverAlert = UsageAlert::where('subject_type', 'server')->sole();
        $this->assertSame('critical', $serverAlert->level);
        $this->assertNull($serverAlert->customer_id);

        $this->actingWith($this->user(Role::Owner, $this->site->customer));
        $list = $this->getJson('/api/v1/usage-alerts')->assertOk();
        $this->assertSame(['bandwidth'], collect($list->json('data'))->pluck('resource')->all());
        $this->assertSame('acme.example', $list->json('data.0.subjectName'));
        $this->assertSame(['warningPercent' => 80, 'criticalPercent' => 95], $list->json('settings'));
        $this->putJson('/api/v1/usage-alerts/settings', ['warningPercent' => 70, 'criticalPercent' => 90])->assertForbidden();

        $this->actingWith($this->user());
        $all = $this->getJson('/api/v1/usage-alerts')->assertOk();
        // The critical alert first.
        $this->assertSame(['disk', 'bandwidth'], collect($all->json('data'))->pluck('resource')->all());
        $this->assertSame('97%', $all->json('data.0.usedText'));
        $this->putJson('/api/v1/usage-alerts/settings', ['warningPercent' => 90, 'criticalPercent' => 80])->assertUnprocessable();
        $this->putJson('/api/v1/usage-alerts/settings', ['warningPercent' => 92, 'criticalPercent' => 99])->assertOk();
        // With the new thresholds, 90% of the bandwidth is 2 points below 92: the alert stays
        // open until 87%.
        $this->report([], ['diskPercent' => 93]);
        $this->assertSame('warning', UsageAlert::where('subject_type', 'server')->sole()->level);
        $this->assertTrue(UsageAlert::where('resource', 'bandwidth')->sole()->isOpen());
    }

    public function test_a_website_with_no_limit_and_a_removed_website_have_no_open_alert(): void
    {
        $this->travelTo(Carbon::parse('2026-11-15 10:00:00', 'UTC'));
        $this->report(['processes' => 99]);
        $alert = UsageAlert::where('resource', 'processes')->sole();
        $this->site->update(['limits' => array_merge($this->site->limits, ['processes' => 0])]);
        $this->report(['processes' => 99]);
        $this->assertFalse($alert->fresh()->isOpen());

        $this->site->update(['limits' => array_merge($this->site->limits, ['processes' => 100])]);
        $this->report(['processes' => 99]);
        $second = UsageAlert::whereNull('resolved_at')->sole();
        $this->site->domains()->delete();
        $this->site->delete();
        $this->travelTo(now()->addDays(9));
        $this->artisan('cloudfactory:prune-usage')->assertSuccessful();
        $this->assertFalse($second->fresh()->isOpen());
        $this->assertSame(0, DB::table('usage_samples')->where('subject_type', 'website')->count());
        // The buckets of 1 hour of the server stay for 400 days.
        $this->assertSame(0, DB::table('usage_samples')->where(['subject_type' => 'server', 'resolution' => 5])->count());
        $this->assertSame(1, DB::table('usage_samples')->where(['subject_type' => 'server', 'resolution' => 60])->count());
    }
}
