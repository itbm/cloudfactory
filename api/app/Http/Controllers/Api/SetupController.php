<?php

namespace App\Http\Controllers\Api;

use App\Enums\Role;
use App\Http\Controllers\Controller;
use App\Models\Server;
use App\Models\User;
use App\Services\PanelCertificate;
use Illuminate\Http\JsonResponse;
use Illuminate\Http\Request;

/**
 * The setup checklist of the panel. A new panel operates before these items are complete, but
 * each open item has an effect that the administrator must know. The checklist reads the state
 * of each item: it changes nothing.
 */
class SetupController extends Controller
{
    public const DONE = 'done';

    public const OPEN = 'open';

    /** The item does not apply to this panel, for example Let's Encrypt for panel.localhost. */
    public const NOT_APPLICABLE = 'not_applicable';

    public function show(Request $request): JsonResponse
    {
        $items = [self::backups(), self::totp($request->user()), self::diskLimits(), self::panelCertificate()];
        $open = count(array_filter($items, fn (array $item) => $item['state'] === self::OPEN));

        return response()->json(['complete' => $open === 0, 'open' => $open, 'items' => $items]);
    }

    /** @return array<string, mixed> */
    private static function backups(): array
    {
        $target = (string) (BackupController::policy()['target'] ?? '');
        $hasTarget = preg_match(BackupController::TARGET, $target) === 1;
        $hasCredentials = BackupController::credentials() !== [];
        $detail = match (true) {
            ! $hasTarget => 'No object storage is set.',
            ! $hasCredentials => "The object storage is {$target}, but it has no credentials.",
            default => "Backups go to {$target}.",
        };

        return self::item(
            'backups',
            'Set the object storage for backups',
            $hasTarget && $hasCredentials,
            'Without object storage, the panel makes no backups. A scheduled backup and a manual backup fail, and you cannot restore a website.',
            $detail,
            'Set the object storage',
            '/backups',
        );
    }

    /** @return array<string, mixed> */
    private static function totp(User $user): array
    {
        $without = User::where('role', Role::Administrator->value)->whereNull('totp_secret')->orderBy('name')->get(['id', 'name', 'email']);
        $own = $without->contains('id', $user->id);
        $names = $without->map(fn (User $u) => "{$u->name} ({$u->email})")->implode(', ');

        return self::item(
            'totp',
            'Turn on two-step sign-in for each administrator',
            $without->isEmpty(),
            'Without two-step sign-in, a person who learns the password of an administrator controls the panel and all its servers.',
            $without->isEmpty() ? 'All administrators use two-step sign-in.' : "These administrators do not use two-step sign-in: {$names}.",
            $own ? 'Turn on two-step sign-in' : 'Open the security settings',
            '/settings',
        );
    }

    /** @return array<string, mixed> */
    private static function diskLimits(): array
    {
        $servers = Server::orderBy('name')->get();
        $without = $servers->filter(function (Server $server) {
            $quotas = collect($server->services ?? [])->firstWhere('name', 'disk-quotas');

            // An agent that reports no state for the disk quotas is an agent of an earlier release.
            return $quotas !== null && ($quotas['state'] ?? '') !== 'running';
        })->values();
        $item = self::item(
            'disk-limits',
            'Turn on disk limits on each server',
            $without->isEmpty(),
            'Without project quotas on the file system, a server does not apply the disk limit of a plan. One website can fill the disk, and then all websites on that server stop.',
            $without->isEmpty()
                ? 'All servers apply the disk limit of each website.'
                : 'These servers do not apply disk limits: '.$without->pluck('name')->implode(', ').'. Mount /srv/cloudfactory on XFS or ext4 with project quotas (prjquota), then restart the agent.',
            'Open the server',
            $without->count() === 1 ? '/servers/'.$without[0]->id : '/servers',
        );
        if ($servers->isEmpty()) {
            $item['state'] = self::NOT_APPLICABLE;
            $item['detail'] = 'No server has joined the panel.';
        }

        return $item;
    }

    /** @return array<string, mixed> */
    private static function panelCertificate(): array
    {
        $current = PanelCertificate::current();
        $domain = $current['domain'];
        $public = PanelCertificate::publicName($domain);
        $trusted = $current['method'] === PanelCertificate::LETS_ENCRYPT && $current['state'] === 'valid';
        $item = self::item(
            'panel-certificate',
            'Use a certificate that browsers trust for the panel',
            $trusted,
            'The panel CA signs the certificate of a new panel. Browsers do not trust the panel CA, so each person who opens the panel sees a security warning.',
            match (true) {
                $trusted => "Let's Encrypt gives the certificate of {$domain}.",
                $current['method'] === null => 'The agent on the panel server has not reported the certificate yet.',
                $current['method'] === PanelCertificate::LETS_ENCRYPT => "The panel waits for the certificate of {$domain} from Let's Encrypt.",
                default => "The panel CA signs the certificate of {$domain}. Change to Let's Encrypt, or add the root certificate of the panel CA to each browser.",
            },
            'Change the panel certificate',
            '/settings/panel',
        );
        if (! $trusted && ! $public) {
            // Let's Encrypt gives no certificate for a domain that is not public.
            $item['state'] = self::NOT_APPLICABLE;
            $item['detail'] = "{$domain} is not a public domain, so only the panel CA can sign its certificate. To remove the warning, add the root certificate of the panel CA to your browser.";
            $item['action']['label'] = 'Get the root certificate';
        }

        return $item;
    }

    /** @return array<string, mixed> */
    private static function item(string $id, string $title, bool $done, string $effect, string $detail, string $label, string $target): array
    {
        return [
            'id' => $id,
            'title' => $title,
            'state' => $done ? self::DONE : self::OPEN,
            // What occurs while the item is open.
            'effect' => $effect,
            // The current state, in words.
            'detail' => $detail,
            // The page of the web panel where the administrator completes the item.
            'action' => ['label' => $label, 'target' => $target],
        ];
    }
}
