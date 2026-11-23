<?php

namespace App\Enums;

/**
 * Spec section 6.4: the agent accepts only typed actions. It never accepts shell commands.
 * Each value is a method of the agent service in agent/proto/cloudfactory/agent/v1/agent.proto.
 */
enum AgentAction: string
{
    case CreateWebsite = 'CreateWebsite';
    case DeleteWebsite = 'DeleteWebsite';
    case SetWebsitePower = 'SetWebsitePower';
    case SetLimits = 'SetLimits';
    case SetRuntime = 'SetRuntime';
    case AddDomain = 'AddDomain';
    case RemoveDomain = 'RemoveDomain';
    case CreateDatabase = 'CreateDatabase';
    case DeleteDatabase = 'DeleteDatabase';
    case SetDatabaseUser = 'SetDatabaseUser';
    case DeleteDatabaseUser = 'DeleteDatabaseUser';
    case Backup = 'Backup';
    case Restore = 'Restore';
    case InstallHostUpdates = 'InstallHostUpdates';
    case Reboot = 'Reboot';
    case UpdateAgent = 'UpdateAgent';
    case PruneBackups = 'PruneBackups';
    case SetSftp = 'SetSftp';
    case ConfigureHost = 'ConfigureHost';
    case SetPanelCertificate = 'SetPanelCertificate';
    case SetPanelBrands = 'SetPanelBrands';
    case SetDatabaseServer = 'SetDatabaseServer';
    case DeleteDatabaseServer = 'DeleteDatabaseServer';
    case SetMailServer = 'SetMailServer';
    case PrepareMailNode = 'PrepareMailNode';
    case SetMailNode = 'SetMailNode';
    case SetMailDomain = 'SetMailDomain';
    case DeleteMailDomain = 'DeleteMailDomain';
    case SetMailbox = 'SetMailbox';
    case DeleteMailbox = 'DeleteMailbox';
    case SetMailAlias = 'SetMailAlias';
    case DeleteMailAlias = 'DeleteMailAlias';
    case SetDnsZones = 'SetDnsZones';
    case DeleteDnsZones = 'DeleteDnsZones';
    case SetCronJobs = 'SetCronJobs';
    case RunCronJob = 'RunCronJob';
    case RunCommand = 'RunCommand';
    case Deploy = 'Deploy';
    case ActivateRelease = 'ActivateRelease';
    case InstallDependencies = 'InstallDependencies';
    case SetMesh = 'SetMesh';
    case RotateMeshKey = 'RotateMeshKey';
    case SendDatabaseServer = 'SendDatabaseServer';
    case ReceiveDatabaseServer = 'ReceiveDatabaseServer';
    case SetPacks = 'SetPacks';
    case InstallWordPress = 'InstallWordPress';
    case CheckWordPress = 'CheckWordPress';
    case UpdateWordPress = 'UpdateWordPress';
    case RestoreWordPress = 'RestoreWordPress';
    case RunWpCli = 'RunWpCli';
    case SetWebsiteSecurity = 'SetWebsiteSecurity';
    case SetCertificates = 'SetCertificates';
    // The import of an account from a different panel (docs/imports.md).
    case FetchImport = 'FetchImport';
    case ImportFiles = 'ImportFiles';
    case ImportDatabase = 'ImportDatabase';
    case ImportMail = 'ImportMail';
    case DeleteImport = 'DeleteImport';

    /** The steps that the agent reports for this action. */
    public function steps(): array
    {
        return match ($this) {
            self::CreateWebsite => ['Create Unix user', 'Copy databases', 'Pull and check image signature', 'Start container in gVisor', 'Configure edge proxy', 'Get TLS certificate'],
            self::DeleteWebsite => ['Stop container', 'Remove edge proxy route', 'Remove Unix user'],
            self::SetWebsitePower => ['Change container state', 'Health check'],
            self::SetLimits => ['Update cgroup limits'],
            self::SetRuntime => ['Check image signature', 'Start new container on a copy', 'Health check', 'Switch traffic', 'Stop old container'],
            self::AddDomain => ['Add edge proxy route', 'Check DNS', 'Get TLS certificate'],
            self::RemoveDomain => ['Remove edge proxy route'],
            self::CreateDatabase => ['Create database', 'Create user', 'Allow the website container only'],
            self::DeleteDatabase => ['Remove access of users', 'Remove database'],
            self::SetDatabaseServer => ['Start database server'],
            self::DeleteDatabaseServer => ['Stop database server', 'Remove data'],
            self::SetDatabaseUser => ['Set user and password', 'Set access to databases'],
            self::DeleteDatabaseUser => ['Remove user'],
            self::Backup => ['Snapshot files', 'Dump databases', 'Upload to object storage', 'Apply retention'],
            self::Restore => ['Download snapshot', 'Restore', 'Health check'],
            self::InstallHostUpdates => ['Download packages', 'Install packages', 'Restart changed services only'],
            self::Reboot => ['Drain the edge proxy', 'Stop containers', 'Reboot', 'Start containers', 'Check all services'],
            self::UpdateAgent => ['Download agent binary', 'Check signature', 'Keep previous binary for rollback', 'Restart agent'],
            self::PruneBackups => ['Remove backups of deleted websites', 'Remove unused data'],
            self::SetSftp => ['Set SFTP access'],
            self::ConfigureHost => ['Save settings', 'Restart websites', 'Restart services'],
            self::SetPanelCertificate => ['Change certificate method', 'Get TLS certificate'],
            self::SetMailServer => ['Start mail server', 'Start webmail', 'Open mail ports', 'Get TLS certificate'],
            self::PrepareMailNode => ['Make keys'],
            self::SetMailNode => ['Make private network', 'Start cluster store', 'Start database'],
            self::SetMailDomain => ['Add domain', 'Make DKIM keys'],
            self::DeleteMailDomain => ['Remove domain'],
            self::SetMailbox => ['Set mailbox'],
            self::DeleteMailbox => ['Remove mailbox and its messages'],
            self::SetMailAlias => ['Set address'],
            self::DeleteMailAlias => ['Remove address'],
            self::SetPanelBrands => ['Set panel domains', 'Get TLS certificates'],
            self::SetDnsZones => ['Start DNS server', 'Write zones', 'Load zones'],
            self::DeleteDnsZones => ['Remove zones'],
            self::SetCronJobs => ['Save the cron jobs'],
            self::RunCronJob => ['Run the command'],
            self::RunCommand => ['Run the command'],
            // The steps after "Build" are the steps of SetRuntime: the agent starts the website
            // from the new release with no stop.
            self::Deploy => ['Get the code', 'Build', 'Migrate the database', ...self::SetRuntime->steps(), 'Remove old releases'],
            self::ActivateRelease => ['Check the release', ...self::SetRuntime->steps()],
            // The agent installs the dependencies in a build container, then starts the website
            // again with no stop.
            self::InstallDependencies => ['Install dependencies', ...self::SetRuntime->steps()],
            self::SetMesh => ['Set tunnels and firewall', 'Check tunnels'],
            self::RotateMeshKey => ['Make new key'],
            self::SendDatabaseServer => ['Stop database server', 'Send data'],
            self::ReceiveDatabaseServer => ['Prepare database server', 'Receive data', 'Start database server'],
            self::SetPacks => ['Check the packs', 'Save the packs and the image policy', 'Load the runtimes'],
            // FR-206: WordPress sites. The agent runs WP-CLI in the container of the website.
            self::InstallWordPress => ['Check the website', 'Create database', 'Download WordPress', 'Install WordPress', 'Check WordPress'],
            self::CheckWordPress => ['Check WordPress'],
            // When a step after "Save a restore point" fails, the agent puts back the restore point.
            self::UpdateWordPress => ['Check WordPress', 'Save a restore point', 'Update WordPress', 'Update plugins', 'Update themes', 'Health check'],
            self::RestoreWordPress => ['Put back the restore point', 'Check WordPress'],
            self::RunWpCli => ['Run the command', 'Check WordPress'],
            self::SetWebsiteSecurity => ['Set edge proxy rules'],
            self::SetCertificates => ['Load certificates', 'Check certificates'],
            // A download for a server that only copies a part has no second step.
            self::FetchImport => ['Download archive', 'Read archive'],
            self::ImportFiles => ['Check archive', 'Copy files'],
            self::ImportDatabase => ['Check archive', 'Load dump'],
            self::ImportMail => ['Check archive', 'Copy messages'],
            self::DeleteImport => ['Remove archive'],
        };
    }

    /** The actions that use the backup repository. The panel adds its credentials when it sends the job. */
    public function usesBackupRepository(): bool
    {
        return match ($this) {
            self::Backup, self::Restore, self::PruneBackups => true,
            default => false,
        };
    }
}
