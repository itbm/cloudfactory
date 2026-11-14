<?php

namespace App\Services;

use App\Models\Customer;
use App\Models\Database;
use App\Models\DatabaseServer;
use App\Models\Plan;
use App\Models\Server;
use App\Models\Website;
use Illuminate\Database\Eloquent\Collection;
use Illuminate\Support\Facades\DB;
use Illuminate\Validation\ValidationException;

/**
 * The rules for the database servers of a customer: the number that the plan of the customer
 * allows, the port of each one, and the database server that a new database gets.
 */
class DatabaseServers
{
    /** The name of a database server: lower-case letters, digits and "-". */
    public const NAME = '/^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$/';

    /**
     * Makes the record of a new database server. The caller sends the database server to the
     * agent: with SetDatabaseServer, or in the payload of a job that makes a database.
     *
     * @param  string  $field  The request field that gets the error text.
     * @param  bool  $checkLimit  False for the move from an earlier release: the databases of
     *                            the customer exist, so they must get a database server.
     *
     * @throws ValidationException
     */
    public static function make(
        Customer $customer,
        Server $server,
        string $engine,
        ?Plan $plan = null,
        ?string $name = null,
        ?array $limits = null,
        string $field = 'engine',
        bool $checkLimit = true,
    ): DatabaseServer {
        self::checkHost($server, $engine, $field);

        return DB::transaction(function () use ($customer, $server, $engine, $plan, $name, $limits, $field, $checkLimit) {
            // The count, the port and the new record are in one transaction. The lock on the
            // customer row stops two requests from passing the limit, or from getting the same
            // port, at the same time.
            $customer = Customer::with('plan')->lockForUpdate()->findOrFail($customer->id);
            if ($checkLimit) {
                self::checkLimit($customer, $field);
            }
            $port = self::freePort($customer->id, $server->id, $engine);
            if ($port === null) {
                throw ValidationException::withMessages([$field => 'A customer can have '.DatabaseServer::PORTS." database servers of one engine on a server. {$customer->name} has this number."]);
            }
            $plan ??= $customer->plan;

            return DatabaseServer::create([
                'customer_id' => $customer->id,
                'server_id' => $server->id,
                'plan_id' => $plan->id,
                'engine' => $engine,
                'name' => $name ?? self::freeName($customer->id, $engine),
                'port' => $port,
                'limits' => $limits ?? $plan->database_limits,
                'status' => DatabaseServer::STARTING,
            ]);
        });
    }

    /**
     * Only an agent that reports database servers can operate one.
     *
     * @throws ValidationException
     */
    public static function checkHost(Server $server, string $engine, string $field = 'engine'): void
    {
        if (! $server->database_instances) {
            throw ValidationException::withMessages([$field => "The agent on the server {$server->name} did not report that it can operate database servers. Update the agent of the server, then try again."]);
        }
        if (! $server->hasDatabaseEngine($engine)) {
            throw ValidationException::withMessages([$field => "The agent on the server {$server->name} did not report that it can operate ".self::engineName($engine).'. Update the agent of the server, then try again.']);
        }
    }

    /**
     * The plan of a customer limits the number of its database servers, as it limits the
     * number of its websites. The caller holds the lock on the customer row.
     *
     * @throws ValidationException
     */
    public static function checkLimit(Customer $customer, string $field = 'engine'): void
    {
        $allowed = $customer->plan->database_servers;
        if ($customer->databaseServers()->count() >= $allowed) {
            throw ValidationException::withMessages([$field => "The {$customer->plan->name} plan allows {$allowed} ".self::plural($allowed).'.']);
        }
        // As for websites, the database servers of the customers of a reseller count against
        // the plan of the reseller, with the database servers of the reseller itself.
        if ($customer->reseller_id || $customer->role === 'reseller') {
            $reseller = $customer->reseller_id ? Customer::with('plan')->lockForUpdate()->findOrFail($customer->reseller_id) : $customer;
            $total = $reseller->plan->database_servers;
            if (DatabaseServer::whereIn('customer_id', self::resellerCustomerIds($reseller))->count() >= $total) {
                throw ValidationException::withMessages([$field => $customer->reseller_id
                    ? "The {$reseller->plan->name} plan of your provider allows {$total} ".self::plural($total).' in total.'
                    : "The {$reseller->plan->name} plan allows {$total} ".self::plural($total).' in total, for you and your customers.']);
            }
        }
    }

    /**
     * The number of database servers that count against the plan of a customer: its own, and
     * for a reseller also those of its customers.
     */
    public static function countFor(Customer $customer): int
    {
        return DatabaseServer::whereIn('customer_id', $customer->role === 'reseller' ? self::resellerCustomerIds($customer) : [$customer->id])->count();
    }

    /** @return list<string> */
    private static function resellerCustomerIds(Customer $reseller): array
    {
        return Customer::where('id', $reseller->id)->orWhere('reseller_id', $reseller->id)->pluck('id')->all();
    }

    /**
     * The smallest free port of an engine on the address of a customer on a server, or null.
     * The first database server of an engine gets the usual port of the engine.
     */
    public static function freePort(string $customerId, string $serverId, string $engine): ?int
    {
        $used = DatabaseServer::where(['customer_id' => $customerId, 'server_id' => $serverId, 'engine' => $engine])->pluck('port')->all();
        $first = DatabaseServer::FIRST_PORT[$engine];
        for ($port = $first; $port < $first + DatabaseServer::PORTS; $port++) {
            if (! in_array($port, $used, true)) {
                return $port;
            }
        }

        return null;
    }

    /** A name that the customer does not use, for example "mariadb-2". */
    public static function freeName(string $customerId, string $engine): string
    {
        $used = DatabaseServer::where('customer_id', $customerId)->pluck('name')->all();
        for ($n = 1; ; $n++) {
            if (! in_array("{$engine}-{$n}", $used, true)) {
                return "{$engine}-{$n}";
            }
        }
    }

    /**
     * The database server that gets a new database of a website.
     *
     * - With an ID, it is that database server. It must be a database server of the customer of
     *   the website, on the server of the website, with the engine of the database.
     * - With no ID, it is the first database server of the engine that has space.
     * - When the customer has no database server of the engine on the server, the panel makes
     *   one, if the plan of the customer permits.
     *
     * The second value is true for a database server that this call made.
     *
     * @return array{0: DatabaseServer, 1: bool}
     *
     * @throws ValidationException
     */
    public static function forDatabase(Website $website, string $engine, ?string $id): array
    {
        $servers = self::forWebsite($website)->where('engine', $engine)->values();
        if ($id !== null) {
            $server = $servers->firstWhere('id', $id);
            if (! $server) {
                throw ValidationException::withMessages(['databaseServerId' => 'Select a database server of the customer of this website, on the server of the website, with the engine of the database.']);
            }

            return [$server, false];
        }
        if ($servers->isEmpty()) {
            return [self::make($website->customer, $website->server, $engine), true];
        }
        $server = $servers->first(fn (DatabaseServer $s) => $s->databases_count < $s->plan->databases);
        if (! $server) {
            throw ValidationException::withMessages(['name' => $servers->count() === 1
                ? "The database server {$servers[0]->name} has {$servers[0]->databases_count} databases, and its plan ({$servers[0]->plan->name}) allows no more. Make a new database server, or give this one a larger plan."
                : 'Each '.self::engineName($engine).' database server of this customer has the number of databases that its plan allows. Make a new database server, or give one a larger plan.']);
        }

        return [$server, false];
    }

    /**
     * The database servers that a website can use: those of its customer on its server. A
     * database server that the agent removes is not one of them.
     *
     * @return Collection<int, DatabaseServer>
     */
    public static function forWebsite(Website $website)
    {
        return DatabaseServer::with('plan')->withCount('databases')
            ->where(['customer_id' => $website->customer_id, 'server_id' => $website->server_id])
            ->where('status', '!=', DatabaseServer::REMOVING)
            ->orderBy('port')->get();
    }

    /**
     * Adds one database to a database server. The caller runs this in a transaction: the lock
     * on the row stops two requests from passing the limit at the same time.
     *
     * @throws ValidationException
     */
    public static function checkSpace(DatabaseServer $server, int $new = 1, string $field = 'name'): DatabaseServer
    {
        $locked = DatabaseServer::with('plan')->lockForUpdate()->findOrFail($server->id);
        if ($locked->status === DatabaseServer::REMOVING) {
            throw ValidationException::withMessages([$field => "The panel removes the database server {$locked->name}. Select a different database server."]);
        }
        $count = $locked->databases()->count();
        if ($count + $new > $locked->plan->databases) {
            throw ValidationException::withMessages([$field => "The {$locked->plan->name} plan allows {$locked->plan->databases} databases in a database server. The database server {$locked->name} has {$count}."]);
        }

        return $locked;
    }

    /**
     * The database server that gets the databases of a website from the shared containers of
     * an earlier release: the database server of the customer with the usual port of the
     * engine. The applications of the website connect to that port. The panel makes the
     * database server when the customer has none on that port.
     *
     * Null when the panel removes the database server on that port now.
     */
    public static function forMove(Website $website, string $engine): ?DatabaseServer
    {
        $server = DatabaseServer::where([
            'customer_id' => $website->customer_id,
            'server_id' => $website->server_id,
            'engine' => $engine,
            'port' => DatabaseServer::FIRST_PORT[$engine],
        ])->first();
        if ($server) {
            return $server->status === DatabaseServer::REMOVING ? null : $server;
        }

        return self::make($website->customer, $website->server, $engine, checkLimit: false);
    }

    public static function engineName(string $engine): string
    {
        return match ($engine) {
            'mariadb' => 'MariaDB',
            'postgresql' => 'PostgreSQL',
            Database::VALKEY => 'Valkey',
            default => $engine,
        };
    }

    private static function plural(int $count): string
    {
        return $count === 1 ? 'database server' : 'database servers';
    }
}
