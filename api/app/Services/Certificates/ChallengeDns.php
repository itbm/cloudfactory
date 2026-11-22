<?php

namespace App\Services\Certificates;

/**
 * A DNS service that can hold the TXT records of the DNS validation (dns-01) of an ACME order.
 *
 * The panel asks each provider in turn (DnsProviders) for the name of each record. The first
 * provider that holds the name for the customer gets the record. The DNS hosting of the panel
 * is the first provider (PanelDns). A provider for a DNS service outside the panel, for example
 * Cloudflare, implements this interface and has the container tag DnsProviders::TAG.
 *
 * The record names are full names, for example "_acme-challenge.example.com". A wildcard name
 * "*.example.com" has the record of "example.com". One name can have two records with
 * different values, for example for "example.com" and "*.example.com".
 */
interface ChallengeDns
{
    /** A short key that is unique among the providers, for example "panel" or "cloudflare". */
    public function key(): string;

    /**
     * True when this provider holds the record name for the customer. The record name is the
     * full name, for example "_acme-challenge.example.com".
     */
    public function holds(string $customerId, string $recordName): bool;

    /**
     * Why the provider cannot use a record name that it holds now, or null. The text is for
     * the user, for example "The registrar does not send the queries for example.com to the
     * name servers of the panel".
     */
    public function problem(string $customerId, string $recordName): ?string;

    /**
     * Adds the TXT records. It gives a state for ready() and cleanUp(), as an array that the
     * panel can keep as JSON.
     *
     * @param  list<array{name: string, value: string}>  $records
     * @return array<string, mixed>
     *
     * @throws AcmeException when the records cannot be added. The message is for the user.
     */
    public function present(string $customerId, array $records): array;

    /**
     * True when the name servers of the zone give the records to the CA now.
     *
     * @param  array<string, mixed>  $state
     */
    public function ready(array $state): bool;

    /**
     * Removes the records. A record that is not there is not an error.
     *
     * @param  list<array{name: string, value: string}>  $records
     * @param  array<string, mixed>  $state
     */
    public function cleanUp(string $customerId, array $records, array $state): void;
}
