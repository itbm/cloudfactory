<?php

use App\Http\Controllers\Agent\AgentController;
use App\Http\Controllers\Agent\JoinController;
use App\Http\Controllers\Api\AccountController;
use App\Http\Controllers\Api\AuditController;
use App\Http\Controllers\Api\AuthController;
use App\Http\Controllers\Api\BackupController;
use App\Http\Controllers\Api\BrandController;
use App\Http\Controllers\Api\CertificateController;
use App\Http\Controllers\Api\CronJobController;
use App\Http\Controllers\Api\CustomerController;
use App\Http\Controllers\Api\DatabaseController;
use App\Http\Controllers\Api\DatabaseServerController;
use App\Http\Controllers\Api\DatabaseToolController;
use App\Http\Controllers\Api\DatabaseUserController;
use App\Http\Controllers\Api\DeployController;
use App\Http\Controllers\Api\DnsAccountController;
use App\Http\Controllers\Api\DnsZoneController;
use App\Http\Controllers\Api\DomainController;
use App\Http\Controllers\Api\FileController;
use App\Http\Controllers\Api\IdentityProviderController;
use App\Http\Controllers\Api\ImageController;
use App\Http\Controllers\Api\ImportController;
use App\Http\Controllers\Api\JobController;
use App\Http\Controllers\Api\LogController;
use App\Http\Controllers\Api\MailAliasController;
use App\Http\Controllers\Api\MailboxController;
use App\Http\Controllers\Api\MailClusterController;
use App\Http\Controllers\Api\MailDomainController;
use App\Http\Controllers\Api\MailServerController;
use App\Http\Controllers\Api\MeshController;
use App\Http\Controllers\Api\NetworkController;
use App\Http\Controllers\Api\NotificationController;
use App\Http\Controllers\Api\NotificationTemplateController;
use App\Http\Controllers\Api\PackController;
use App\Http\Controllers\Api\PanelCertificateController;
use App\Http\Controllers\Api\PasskeyController;
use App\Http\Controllers\Api\PasswordResetController;
use App\Http\Controllers\Api\PlanController;
use App\Http\Controllers\Api\PluginController;
use App\Http\Controllers\Api\PreviewController;
use App\Http\Controllers\Api\RolloutController;
use App\Http\Controllers\Api\RuntimeController;
use App\Http\Controllers\Api\ServerController;
use App\Http\Controllers\Api\SetupController;
use App\Http\Controllers\Api\SftpController;
use App\Http\Controllers\Api\SsoController;
use App\Http\Controllers\Api\TrustedKeyController;
use App\Http\Controllers\Api\UsageController;
use App\Http\Controllers\Api\UserController;
use App\Http\Controllers\Api\WebsiteController;
use Illuminate\Support\Facades\Route;

/*
| The public API (spec section 6.3, FR-103). The web panel and the cf command line tool use only
| these routes. openapi/openapi.yaml describes each route, and a test checks that they agree.
*/

Route::prefix('v1')->group(function () {
    // Adversarial review, finding A1-07: the third value of a throttle is the name of its
    // counter. Without a name, all limits of one client would share one counter.
    Route::post('auth/login', [AuthController::class, 'login'])->middleware('throttle:10,1,post-auth-login');
    // The ways to sign in that the sign-in page shows.
    Route::get('auth/methods', [AuthController::class, 'methods'])->middleware('throttle:60,1,get-auth-methods');
    // A user who does not know the password asks for an email with a link to set a new one.
    Route::post('auth/password-reset', [PasswordResetController::class, 'request'])->middleware('throttle:5,1,post-auth-password-reset');
    Route::post('auth/password-reset/confirm', [PasswordResetController::class, 'reset'])->middleware('throttle:10,1,post-auth-password-reset-confirm');
    // FR-103: sign in with a passkey, in place of the password and the TOTP code.
    Route::post('auth/passkey/options', [PasskeyController::class, 'signInOptions'])->middleware('throttle:30,1,post-auth-passkey-options');
    Route::post('auth/passkey', [PasskeyController::class, 'signIn'])->middleware('throttle:10,1,post-auth-passkey');
    // Single sign-on: the browser goes to the identity provider and comes back (SsoController).
    Route::get('auth/sso/oidc/callback', [SsoController::class, 'callback'])->middleware('throttle:30,1,get-auth-sso-oidc-callback');
    Route::post('auth/sso/saml/acs', [SsoController::class, 'acs'])->middleware('throttle:30,1,post-auth-sso-saml-acs');
    Route::get('auth/sso/saml/metadata', [SsoController::class, 'metadata'])->middleware('throttle:60,1,get-auth-sso-saml-metadata');
    Route::get('auth/sso/{id}', [SsoController::class, 'start'])->middleware('throttle:30,1,get-auth-sso-x');
    Route::post('auth/sso/complete', [SsoController::class, 'complete'])->middleware('throttle:10,1,post-auth-sso-complete');
    // White label: the brand of the sign-in page, for the domain of the request.
    Route::get('brand', [BrandController::class, 'show'])->middleware('throttle:60,1,get-brand');
    // The webhook of a website: a Git host calls it after a push. The token in the address is
    // the only credential.
    Route::post('hooks/deploy/{token}', [DeployController::class, 'hook'])->middleware('throttle:30,1,post-hooks-deploy-x');

    Route::middleware(['auth:sanctum', 'active'])->group(function () {
        Route::post('auth/logout', [AuthController::class, 'logout']);
        Route::get('me', [AuthController::class, 'me']);
        // R-01: the user changes the password and turns TOTP on or off. Only a sign-in session can.
        Route::middleware('throttle:10,1,auth-changes')->group(function () {
            Route::put('me/password', [AuthController::class, 'changePassword']);
            Route::post('me/totp/setup', [AuthController::class, 'setupTotp']);
            Route::post('me/totp/enable', [AuthController::class, 'enableTotp']);
            Route::post('me/totp/disable', [AuthController::class, 'disableTotp']);
            // FR-103: the passkeys of the user.
            Route::post('me/passkeys/options', [PasskeyController::class, 'options']);
            Route::post('me/passkeys', [PasskeyController::class, 'store']);
            Route::patch('me/passkeys/{id}', [PasskeyController::class, 'update']);
            Route::delete('me/passkeys/{id}', [PasskeyController::class, 'destroy']);
            Route::post('me/identities/{id}', [SsoController::class, 'connect']);
            Route::delete('me/identities/{id}', [SsoController::class, 'disconnect']);
        });
        Route::get('me/passkeys', [PasskeyController::class, 'index']);
        // Single sign-on: the accounts of identity providers that the user connected.
        Route::get('me/identities', [SsoController::class, 'identities']);
        Route::middleware('scope:tokens')->group(function () {
            Route::get('tokens', [AuthController::class, 'tokens']);
            Route::post('tokens', [AuthController::class, 'createToken'])->middleware('throttle:10,1,post-tokens');
            Route::delete('tokens/{id}', [AuthController::class, 'revokeToken'])->whereNumber('id');
        });

        // Adversarial review, finding A2-06: the jobs have no scope of their own. The controller
        // gives each kind of job only to a token with the scope of that kind.
        Route::get('jobs', [JobController::class, 'index']);
        Route::get('jobs/{id}', [JobController::class, 'show']);

        Route::middleware('scope:websites')->group(function () {
            Route::get('websites', [WebsiteController::class, 'index']);
            Route::post('websites', [WebsiteController::class, 'store']);
            Route::get('websites/{id}', [WebsiteController::class, 'show']);
            Route::patch('websites/{id}', [WebsiteController::class, 'update']);
            Route::delete('websites/{id}', [WebsiteController::class, 'destroy']);
            Route::post('websites/{id}/power', [WebsiteController::class, 'power']);
            Route::post('websites/{id}/retry', [WebsiteController::class, 'retry']);
            Route::post('websites/{id}/staging', [WebsiteController::class, 'staging']);
            // Audit finding S5: limits for each customer (AppServiceProvider).
            Route::post('websites/{websiteId}/domains', [DomainController::class, 'store'])->middleware('throttle:domain-changes');
            Route::post('websites/{websiteId}/domains/{name}/verify', [DomainController::class, 'verify'])->middleware('throttle:domain-checks');
            Route::post('websites/{websiteId}/domains/{name}/primary', [DomainController::class, 'makePrimary']);
            Route::post('websites/{websiteId}/domains/{name}/check', [DomainController::class, 'check'])->middleware('throttle:domain-checks');
            Route::delete('websites/{websiteId}/domains/{name}', [DomainController::class, 'destroy']);
            // The certificates of the customers: uploads, and certificates with DNS validation
            // (docs/certificates.md). Each order uses the limits of the certificate authority.
            Route::get('certificates', [CertificateController::class, 'index']);
            Route::post('certificates', [CertificateController::class, 'store'])->middleware('throttle:certificate-changes');
            Route::get('certificates/{id}', [CertificateController::class, 'show']);
            Route::put('certificates/{id}', [CertificateController::class, 'update'])->middleware('throttle:certificate-changes');
            Route::post('certificates/{id}/renew', [CertificateController::class, 'renew'])->middleware('throttle:certificate-changes');
            Route::delete('certificates/{id}', [CertificateController::class, 'destroy']);
            Route::put('websites/{websiteId}/sftp', [SftpController::class, 'update']);
            Route::put('websites/{websiteId}/runtime', [RuntimeController::class, 'update']);
            Route::post('websites/{websiteId}/runtime/revert', [RuntimeController::class, 'revert']);
            Route::post('websites/{websiteId}/runtime/install', [RuntimeController::class, 'installDependencies']);
            Route::patch('websites/{websiteId}/environment', [RuntimeController::class, 'environment']);

            // The file manager and the logs. The agent does each request as the Unix user of
            // the website, and gives the answer at once.
            Route::get('websites/{websiteId}/files', [FileController::class, 'index'])->middleware('throttle:site-tools');
            Route::delete('websites/{websiteId}/files', [FileController::class, 'destroy'])->middleware('throttle:site-tools');
            Route::get('websites/{websiteId}/files/content', [FileController::class, 'show'])->middleware('throttle:site-tools');
            Route::put('websites/{websiteId}/files/content', [FileController::class, 'store'])->middleware('throttle:site-tools');
            Route::post('websites/{websiteId}/files/directories', [FileController::class, 'makeDirectory'])->middleware('throttle:site-tools');
            Route::post('websites/{websiteId}/files/move', [FileController::class, 'move'])->middleware('throttle:site-tools');
            Route::get('websites/{websiteId}/logs', [LogController::class, 'index'])->middleware('throttle:site-tools');
            Route::get('websites/{websiteId}/usage', [UsageController::class, 'website']);
            Route::get('usage-alerts', [UsageController::class, 'alerts']);

            Route::get('websites/{websiteId}/cron-jobs', [CronJobController::class, 'index']);
            Route::post('websites/{websiteId}/cron-jobs', [CronJobController::class, 'store']);
            Route::patch('websites/{websiteId}/cron-jobs/{cronJobId}', [CronJobController::class, 'update']);
            Route::delete('websites/{websiteId}/cron-jobs/{cronJobId}', [CronJobController::class, 'destroy']);
            Route::post('websites/{websiteId}/cron-jobs/{cronJobId}/run', [CronJobController::class, 'run'])->middleware('throttle:30,1,post-websites-x-cron-jobs-x-run');

            // Deploys from a Git repository.
            Route::get('websites/{websiteId}/deploy', [DeployController::class, 'show']);
            Route::put('websites/{websiteId}/deploy', [DeployController::class, 'update']);
            Route::delete('websites/{websiteId}/deploy', [DeployController::class, 'destroy']);
            Route::post('websites/{websiteId}/deploy/webhook', [DeployController::class, 'newWebhook']);
            Route::put('websites/{websiteId}/deploy/secrets/{name}', [DeployController::class, 'setSecret']);
            Route::delete('websites/{websiteId}/deploy/secrets/{name}', [DeployController::class, 'removeSecret']);
            Route::post('websites/{websiteId}/deploy/releases', [DeployController::class, 'start'])->middleware('throttle:30,1,post-websites-x-deploy-releases');
            Route::post('websites/{websiteId}/deploy/releases/{releaseId}/activate', [DeployController::class, 'activate'])->middleware('throttle:30,1,post-websites-x-deploy-releases-x-activate');
            // The preview websites of the pull requests (App\Services\Previews).
            Route::get('websites/{websiteId}/previews', [PreviewController::class, 'index']);
            Route::put('websites/{websiteId}/previews', [PreviewController::class, 'update']);
            Route::post('websites/{websiteId}/previews', [PreviewController::class, 'store'])->middleware('throttle:30,1,post-websites-x-previews');
            Route::delete('websites/{websiteId}/previews/{number}', [PreviewController::class, 'destroy'])->whereNumber('number');
            Route::get('runtime-images', [ImageController::class, 'runtimes']);
        });

        Route::middleware('scope:databases')->group(function () {
            Route::get('websites/{websiteId}/databases', [DatabaseController::class, 'index']);
            Route::post('websites/{websiteId}/databases', [DatabaseController::class, 'store']);
            Route::delete('websites/{websiteId}/databases/{databaseId}', [DatabaseController::class, 'destroy']);
            Route::post('websites/{websiteId}/database-users', [DatabaseUserController::class, 'store']);
            Route::patch('websites/{websiteId}/database-users/{userId}', [DatabaseUserController::class, 'update']);
            Route::delete('websites/{websiteId}/database-users/{userId}', [DatabaseUserController::class, 'destroy']);
            // The database tool (Adminer): open a database with a temporary database user.
            Route::post('websites/{websiteId}/databases/{databaseId}/adminer', [DatabaseToolController::class, 'store'])->middleware('throttle:30,1,post-websites-x-databases-x-adminer');
            Route::get('adminer-logins/{id}', [DatabaseToolController::class, 'show']);
            // The database servers of a customer: containers with their own limits, as websites.
            // The private networks of the customers (VPC).
            Route::get('networks', [NetworkController::class, 'index']);
            Route::post('networks', [NetworkController::class, 'store']);
            Route::get('networks/{id}', [NetworkController::class, 'show']);
            Route::patch('networks/{id}', [NetworkController::class, 'update']);
            Route::delete('networks/{id}', [NetworkController::class, 'destroy']);
            Route::get('database-servers', [DatabaseServerController::class, 'index']);
            Route::post('database-servers', [DatabaseServerController::class, 'store']);
            Route::get('database-servers/{id}', [DatabaseServerController::class, 'show']);
            Route::patch('database-servers/{id}', [DatabaseServerController::class, 'update']);
            Route::delete('database-servers/{id}', [DatabaseServerController::class, 'destroy']);
            Route::post('database-servers/{id}/retry', [DatabaseServerController::class, 'retry']);
            Route::post('database-servers/{id}/move', [DatabaseServerController::class, 'move']);
            Route::get('database-servers/{id}/usage', [UsageController::class, 'databaseServer']);
        });

        // DNS hosting: the zones of a customer and their records.
        Route::middleware('scope:dns')->group(function () {
            Route::get('dns-zones', [DnsZoneController::class, 'index']);
            Route::post('dns-zones', [DnsZoneController::class, 'store'])->middleware('throttle:dns-changes');
            Route::get('dns-zones/{id}', [DnsZoneController::class, 'show']);
            Route::delete('dns-zones/{id}', [DnsZoneController::class, 'destroy'])->middleware('throttle:dns-changes');
            Route::post('dns-zones/{id}/records', [DnsZoneController::class, 'storeRecord'])->middleware('throttle:dns-changes');
            Route::patch('dns-zones/{id}/records/{recordId}', [DnsZoneController::class, 'updateRecord'])->middleware('throttle:dns-changes');
            Route::delete('dns-zones/{id}/records/{recordId}', [DnsZoneController::class, 'destroyRecord'])->middleware('throttle:dns-changes');
            Route::put('dns-zones/{id}/dnssec', [DnsZoneController::class, 'dnssec'])->middleware('throttle:dns-changes');
            Route::post('dns-zones/{id}/check', [DnsZoneController::class, 'check'])->middleware('throttle:domain-checks');
            Route::post('dns-zones/{id}/sync', [DnsZoneController::class, 'sync'])->middleware('throttle:dns-changes');
            Route::post('dns-zones/{id}/import', [DnsZoneController::class, 'import'])->middleware('throttle:dns-changes');
            Route::get('dns-zones/{id}/export', [DnsZoneController::class, 'export']);
            Route::post('dns-zones/{id}/move', [DnsZoneController::class, 'move'])->middleware('throttle:dns-changes');
            Route::get('dns-accounts', [DnsAccountController::class, 'index']);
            Route::post('dns-accounts', [DnsAccountController::class, 'store'])->middleware('throttle:dns-changes');
            Route::patch('dns-accounts/{id}', [DnsAccountController::class, 'update'])->middleware('throttle:dns-changes');
            Route::delete('dns-accounts/{id}', [DnsAccountController::class, 'destroy'])->middleware('throttle:dns-changes');
        });

        // Email: the mail domains of a customer, with their mailboxes, aliases and forwarders.
        Route::middleware('scope:mail')->group(function () {
            Route::get('mail/domains', [MailDomainController::class, 'index']);
            Route::post('mail/domains', [MailDomainController::class, 'store'])->middleware('throttle:domain-changes');
            Route::get('mail/domains/{id}', [MailDomainController::class, 'show']);
            Route::post('mail/domains/{id}/check', [MailDomainController::class, 'check'])->middleware('throttle:domain-checks');
            Route::post('mail/domains/{id}/retry', [MailDomainController::class, 'retry'])->middleware('throttle:mail-changes');
            Route::delete('mail/domains/{id}', [MailDomainController::class, 'destroy'])->middleware('throttle:mail-changes');
            Route::get('mail/mailboxes', [MailboxController::class, 'index']);
            Route::post('mail/mailboxes', [MailboxController::class, 'store'])->middleware('throttle:mail-changes');
            Route::patch('mail/mailboxes/{id}', [MailboxController::class, 'update'])->middleware('throttle:mail-changes');
            Route::get('mail/mailboxes/{id}/private-key', [MailboxController::class, 'privateKey']);
            Route::delete('mail/mailboxes/{id}', [MailboxController::class, 'destroy'])->middleware('throttle:mail-changes');
            Route::get('mail/aliases', [MailAliasController::class, 'index']);
            Route::post('mail/aliases', [MailAliasController::class, 'store'])->middleware('throttle:mail-changes');
            Route::delete('mail/aliases/{id}', [MailAliasController::class, 'destroy'])->middleware('throttle:mail-changes');
        });

        Route::middleware('scope:backups')->group(function () {
            Route::get('websites/{websiteId}/snapshots', [BackupController::class, 'index']);
            Route::post('websites/{websiteId}/snapshots', [BackupController::class, 'store'])->middleware('throttle:20,1,post-snapshots');
            Route::post('websites/{websiteId}/snapshots/{snapshotId}/restore', [BackupController::class, 'restore'])->middleware('throttle:20,1,post-restore');
            Route::get('backup-policy', [BackupController::class, 'showPolicy']);
            Route::put('backup-policy', [BackupController::class, 'updatePolicy'])->middleware('admin');
            Route::get('backup-policy/repository-password', [BackupController::class, 'showRepositoryPassword'])->middleware(['admin', 'throttle:10,1,get-repository-password']);
            // Second audit, finding F08: the key of one server for the object storage.
            Route::put('backup-policy/servers/{serverId}/credentials', [BackupController::class, 'updateServerCredentials'])->middleware('admin');
            Route::delete('backup-policy/servers/{serverId}/credentials', [BackupController::class, 'destroyServerCredentials'])->middleware('admin');
        });

        Route::middleware(['admin', 'scope:servers'])->group(function () {
            Route::get('servers', [ServerController::class, 'index']);
            Route::post('servers/join-tokens', [ServerController::class, 'joinToken']);
            Route::get('servers/{id}', [ServerController::class, 'show']);
            Route::patch('servers/{id}', [ServerController::class, 'update']);
            Route::post('servers/{id}/install-updates', [ServerController::class, 'installUpdates']);
            Route::post('servers/{id}/reboot', [ServerController::class, 'reboot']);
            Route::post('servers/{id}/update-agent', [ServerController::class, 'updateAgent']);
            Route::post('servers/{id}/check-connection', [ServerController::class, 'checkConnection']);
            // Turns on email for a server with the email role.
            Route::put('servers/{id}/mail', [ServerController::class, 'setMail']);
            // Mail clusters: mail servers on three or five servers that share their data.
            Route::get('mail-clusters', [MailClusterController::class, 'index']);
            Route::post('mail-clusters', [MailClusterController::class, 'store']);
            Route::get('mail-clusters/{id}', [MailClusterController::class, 'show']);
            Route::post('mail-clusters/{id}/retry', [MailClusterController::class, 'retry']);
            Route::delete('servers/{id}', [ServerController::class, 'destroy']);
            Route::get('servers/{id}/usage', [UsageController::class, 'server']);
            Route::put('usage-alerts/settings', [UsageController::class, 'updateSettings']);
            // The mesh: the encrypted tunnels between the servers.
            Route::get('mesh', [MeshController::class, 'show']);
            Route::patch('mesh', [MeshController::class, 'update']);
            Route::patch('mesh/servers/{id}', [MeshController::class, 'updateServer']);
            Route::post('mesh/servers/{id}/rotate-key', [MeshController::class, 'rotateKey']);
            Route::post('mesh/tunnels', [MeshController::class, 'storeTunnel']);
            Route::post('mesh/tunnels/{id}/repair', [MeshController::class, 'repairTunnel']);
            Route::delete('mesh/tunnels/{id}', [MeshController::class, 'destroyTunnel']);
            // The TLS certificate of the panel domain: the CA of the panel or Let's Encrypt.
            Route::get('panel-certificate', [PanelCertificateController::class, 'show']);
            Route::put('panel-certificate', [PanelCertificateController::class, 'update'])->middleware('throttle:10,1,put-panel-certificate');
            // The setup checklist of the panel, with the effect of each open item.
            Route::get('setup', [SetupController::class, 'show']);
        });

        // The import of an account from cPanel, DirectAdmin, Plesk or HestiaCP (docs/imports.md).
        // The import makes websites, databases, mail and DNS zones, so only an administrator
        // imports.
        Route::middleware(['admin', 'scope:websites'])->group(function () {
            Route::get('imports', [ImportController::class, 'index']);
            Route::post('imports', [ImportController::class, 'store'])->middleware('throttle:20,1,post-imports');
            Route::get('imports/{id}', [ImportController::class, 'show']);
            Route::put('imports/{id}/archive', [ImportController::class, 'upload']);
            Route::post('imports/{id}/start', [ImportController::class, 'start']);
            Route::delete('imports/{id}', [ImportController::class, 'destroy']);
        });

        Route::middleware(['admin', 'scope:images'])->group(function () {
            Route::get('images', [ImageController::class, 'index']);
            Route::post('images', [ImageController::class, 'store']);
            Route::patch('images/{id}', [ImageController::class, 'update']);
            Route::post('images/{id}/check', [ImageController::class, 'check']);
            Route::get('rollouts', [RolloutController::class, 'index']);
            Route::post('rollouts', [RolloutController::class, 'store']);
            Route::get('rollouts/{id}', [RolloutController::class, 'show']);
            Route::post('rollouts/{id}/pause', [RolloutController::class, 'pause']);
            Route::post('rollouts/{id}/resume', [RolloutController::class, 'resume']);
            Route::post('rollouts/{id}/roll-back', [RolloutController::class, 'rollBack']);
        });

        Route::middleware('scope:customers')->group(function () {
            // The customer of the signed-in user, with its plan, its usage and its users.
            Route::get('account', [AccountController::class, 'show']);
            Route::get('customers', [CustomerController::class, 'index']);
            Route::post('customers', [CustomerController::class, 'store']);
            Route::patch('customers/{id}', [CustomerController::class, 'update'])->middleware('throttle:customer-changes');
            Route::get('plans', [PlanController::class, 'index']);
            Route::post('plans', [PlanController::class, 'store'])->middleware('admin');
            // White label: the default brand of the panel.
            Route::get('brand/default', [BrandController::class, 'showDefault'])->middleware('admin');
            Route::put('brand/default', [BrandController::class, 'updateDefault'])->middleware('admin');
            Route::patch('plans/{id}', [PlanController::class, 'update'])->middleware('admin');
        });

        // Audit finding B5: the users who can sign in.
        Route::middleware('scope:users')->group(function () {
            Route::get('users', [UserController::class, 'index']);
            Route::post('users', [UserController::class, 'store'])->middleware('throttle:30,1,post-users');
            Route::post('users/{id}/reset-password', [UserController::class, 'resetPassword'])->whereNumber('id')->middleware('throttle:30,1,post-users-x-reset-password');
            Route::delete('users/{id}', [UserController::class, 'destroy'])->whereNumber('id');
        });
        // Single sign-on: the identity providers of the panel (IdentityProviderController).
        Route::middleware(['admin', 'scope:users'])->group(function () {
            Route::get('identity-providers', [IdentityProviderController::class, 'index']);
            Route::post('identity-providers', [IdentityProviderController::class, 'store'])->middleware('throttle:10,1,post-identity-providers');
            Route::put('identity-providers/{id}', [IdentityProviderController::class, 'update'])->middleware('throttle:10,1,put-identity-providers-x');
            Route::delete('identity-providers/{id}', [IdentityProviderController::class, 'destroy']);
        });

        Route::get('audit', [AuditController::class, 'index'])->middleware('scope:audit');

        // Plugins: programs outside the panel with their own token (PluginController).
        Route::middleware(['admin', 'scope:plugins'])->group(function () {
            Route::get('plugins', [PluginController::class, 'index']);
            Route::post('plugins/preview', [PluginController::class, 'preview']);
            Route::post('plugins', [PluginController::class, 'store'])->middleware('throttle:10,1,post-plugins');
            Route::get('plugin-settings', [PluginController::class, 'settings']);
            Route::put('plugin-settings', [PluginController::class, 'updateSettings'])->middleware('throttle:10,1,put-plugin-settings');
            Route::get('plugins/{id}', [PluginController::class, 'show']);
            Route::put('plugins/{id}', [PluginController::class, 'upgrade'])->middleware('throttle:10,1,put-plugins-x');
            Route::patch('plugins/{id}', [PluginController::class, 'update']);
            Route::delete('plugins/{id}', [PluginController::class, 'destroy']);
            Route::post('plugins/{id}/credentials', [PluginController::class, 'rotate'])->middleware('throttle:10,1,post-plugins-x-credentials');
            Route::post('plugins/{id}/container', [PluginController::class, 'restart'])->middleware('throttle:10,1,post-plugins-x-container');
            Route::post('plugins/{id}/test', [PluginController::class, 'test'])->middleware('throttle:30,1,post-plugins-x-test');
            Route::get('plugins/{id}/deliveries', [PluginController::class, 'deliveries']);
            Route::post('plugins/{id}/deliveries/{deliveryId}/retry', [PluginController::class, 'retry'])->middleware('throttle:30,1,post-plugins-x-deliveries-x-retry');
        });
        // The public keys of the publishers of plugins and packs (TrustedKeyController).
        Route::middleware(['admin', 'scope:plugins,packs'])->group(function () {
            Route::get('trusted-keys', [TrustedKeyController::class, 'index']);
            Route::post('trusted-keys', [TrustedKeyController::class, 'store'])->middleware('throttle:10,1,post-trusted-keys');
            Route::delete('trusted-keys/{id}', [TrustedKeyController::class, 'destroy']);
        });
        // Packs: signed manifests that add a website runtime (PackController, docs/packs.md).
        Route::middleware(['admin', 'scope:packs'])->group(function () {
            Route::get('packs', [PackController::class, 'index']);
            Route::post('packs/preview', [PackController::class, 'preview'])->middleware('throttle:30,1,post-packs-preview');
            Route::post('packs', [PackController::class, 'store'])->middleware('throttle:10,1,post-packs');
            Route::post('packs/sync', [PackController::class, 'sync'])->middleware('throttle:10,1,post-packs-sync');
            Route::get('pack-settings', [PackController::class, 'settings']);
            Route::put('pack-settings', [PackController::class, 'updateSettings'])->middleware('throttle:10,1,put-pack-settings');
            Route::get('packs/{id}', [PackController::class, 'show']);
            Route::put('packs/{id}', [PackController::class, 'update'])->middleware('throttle:10,1,put-packs-x');
            Route::delete('packs/{id}', [PackController::class, 'destroy']);
        });
        // The pages of the plugins for the menu, and a short token for a page. Each role.
        Route::get('plugin-pages', [PluginController::class, 'pages']);
        Route::post('plugin-pages/{id}/{pageId}/session', [PluginController::class, 'pageSession'])->middleware('throttle:60,1,post-plugin-pages-x-x-session');
        // The actions of the plugins (buttons on websites, servers and customers). Each role.
        Route::get('plugin-actions', [PluginController::class, 'actions']);
        Route::post('plugin-actions/{id}/{actionId}', [PluginController::class, 'runAction'])->middleware('throttle:30,1,post-plugin-actions-x-x');
        // Email notifications: the events that each user receives, the emails that the panel
        // sent, and for administrators and resellers the SMTP server and the templates.
        Route::middleware('scope:notifications')->group(function () {
            Route::get('me/notification-preferences', [NotificationController::class, 'preferences']);
            Route::put('me/notification-preferences', [NotificationController::class, 'updatePreferences']);
            Route::get('notifications', [NotificationController::class, 'index']);
            Route::get('notifications/{id}', [NotificationController::class, 'show']);
            Route::post('notifications/{id}/retry', [NotificationController::class, 'retry'])->middleware('throttle:30,1,post-notifications-x-retry');
            Route::get('mail-server', [MailServerController::class, 'show']);
            Route::put('mail-server', [MailServerController::class, 'update']);
            Route::delete('mail-server', [MailServerController::class, 'destroy']);
            Route::post('mail-server/test', [MailServerController::class, 'test'])->middleware('throttle:10,1,post-mail-server-test');
            Route::get('notification-templates', [NotificationTemplateController::class, 'index']);
            Route::put('notification-templates/{event}', [NotificationTemplateController::class, 'update']);
            Route::delete('notification-templates/{event}', [NotificationTemplateController::class, 'destroy']);
            Route::post('notification-templates/{event}/preview', [NotificationTemplateController::class, 'preview'])->middleware('throttle:60,1,post-notification-templates-x-preview');
        });
    });
});

/*
| Routes for agents (spec section 6.4). The agent uses its own token, not a user token.
*/
Route::prefix('agent/v1')->group(function () {
    Route::post('join', JoinController::class)->middleware('throttle:20,1,post-join');

    Route::middleware('agent')->group(function () {
        Route::post('jobs/{id}/events', [AgentController::class, 'jobEvent']);
        Route::post('report', [AgentController::class, 'report']);
        // The uploaded archive of an import, for a server with a download job of the import.
        Route::get('imports/{id}/archive', [ImportController::class, 'archive']);
    });
});
