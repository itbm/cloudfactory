<?php

namespace App\Services\Notifications;

use App\Enums\NotificationEvent;
use App\Models\Customer;
use App\Models\NotificationTemplate;
use App\Models\User;

/**
 * The subject and the text of each event. A reseller can change them for its customers, and an
 * administrator for the panel. The panel uses, in this sequence: the template of the reseller,
 * the template of the panel, and the default template.
 */
class Templates
{
    public const SUBJECT_MAX = 200;

    public const BODY_MAX = 10000;

    /** A word in a template: {{name}}. Spaces in the braces are permitted. */
    public const VARIABLE = '/\{\{\s*([A-Za-z]+)\s*\}\}/';

    /**
     * The template that a scope uses for an event, and where it comes from.
     *
     * @return array{subject: string, body: string, source: 'default'|'panel'|'reseller'}
     */
    public static function effective(NotificationEvent $event, ?Customer $reseller): array
    {
        $rows = NotificationTemplate::where('event', $event->value)
            ->where(fn ($q) => $q->whereNull('customer_id')->when($reseller, fn ($q) => $q->orWhere('customer_id', $reseller->id)))
            ->get()
            ->keyBy(fn (NotificationTemplate $t) => $t->customer_id ?? 'panel');
        if ($reseller && ($own = $rows->get($reseller->id))) {
            return ['subject' => $own->subject, 'body' => $own->body, 'source' => 'reseller'];
        }
        if ($panel = $rows->get('panel')) {
            return ['subject' => $panel->subject, 'body' => $panel->body, 'source' => 'panel'];
        }

        return ['subject' => $event->defaultSubject(), 'body' => $event->defaultBody(), 'source' => 'default'];
    }

    /**
     * The words of a template that the event does not have.
     *
     * @return list<string>
     */
    public static function unknownVariables(NotificationEvent $event, string $text): array
    {
        preg_match_all(self::VARIABLE, $text, $m);

        return array_values(array_unique(array_diff($m[1], array_keys($event->variables()))));
    }

    /** @param array<string, string> $values */
    public static function fill(string $template, array $values, bool $oneLine = false): string
    {
        $text = (string) preg_replace_callback(self::VARIABLE, function (array $m) use ($values, $oneLine) {
            $value = (string) ($values[$m[1]] ?? '');

            return $oneLine ? trim((string) preg_replace('/[\r\n]+/', ' ', $value)) : $value;
        }, $template);

        return $oneLine ? mb_substr(trim((string) preg_replace('/[\r\n]+/', ' ', $text)), 0, 300) : $text;
    }

    /**
     * The values for one user: the name of the user, the brand and the address of the panel,
     * with the values of the event.
     *
     * @param  array<string, scalar|null>  $values
     * @return array<string, string>
     */
    public static function valuesFor(User $user, array $values): array
    {
        $brand = Senders::brand(Senders::brandOwner($user));

        return array_map(fn ($v) => (string) $v, ['name' => $user->name, 'brand' => $brand['name'], 'panelUrl' => $brand['url']] + $values);
    }

    /**
     * The HTML part of an email: the text, with a heading in the colour of the brand. Each line
     * is escaped, and each address becomes a link.
     *
     * @param  array{name: string, colour: string}  $brand
     */
    public static function html(string $text, array $brand): string
    {
        $paragraphs = preg_split('/\n{2,}/', trim($text)) ?: [];
        $body = '';
        foreach ($paragraphs as $p) {
            $escaped = nl2br(htmlspecialchars($p, ENT_QUOTES | ENT_HTML5, 'UTF-8'), false);
            $escaped = (string) preg_replace('#\bhttps?://[^\s<]+#', '<a href="$0" style="color:'.$brand['colour'].'">$0</a>', $escaped);
            $body .= '<p style="margin:0 0 14px">'.$escaped."</p>\n";
        }
        $name = htmlspecialchars($brand['name'], ENT_QUOTES | ENT_HTML5, 'UTF-8');

        return <<<HTML
        <!doctype html>
        <html><body style="margin:0;padding:24px;background:#f4f5f7;font:15px/1.5 -apple-system,Segoe UI,Roboto,sans-serif;color:#1f2933">
        <div style="max-width:560px;margin:0 auto;background:#fff;border-radius:8px;overflow:hidden;border:1px solid #e1e4e8">
        <div style="padding:14px 24px;background:{$brand['colour']};color:#fff;font-weight:600">{$name}</div>
        <div style="padding:24px">
        {$body}</div></div>
        <p style="max-width:560px;margin:12px auto 0;color:#6b7280;font-size:12px">To choose the emails that you get, open Settings, then Notifications, in the panel.</p>
        </body></html>
        HTML;
    }
}
