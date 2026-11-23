<?php

namespace App\Enums;

/**
 * Spec section 6.4: the agent accepts only typed actions. It never accepts shell commands.
 * Each value is a method of the agent service in agent/proto/cloudfactory/agent/v1/agent.proto.
 * Keep the cases and the steps in alphabetical order, so that two pull requests that add actions
 * do not conflict (AgentActionOrderTest).
 */
enum AgentAction: string
{
    case ActivateRelease = 'ActivateRelease';
    case AddDomain = 'AddDomain';
    case Backup = 'Backup';
    case CheckWordPress = 'CheckWordPress';
    case ConfigureHost = 'ConfigureHost';
    case CreateDatabase = 'CreateDatabase';
    case CreateWebsite = 'CreateWebsite';
    case DeleteDatabase = 'DeleteDatabase';
    case DeleteDatabaseServer = 'DeleteDatabaseServer';
    case DeleteDatabaseUser = 'DeleteDatabaseUser';
    case DeleteDnsZones = 'DeleteDnsZones';
    case DeleteImport = 'DeleteImport';
    case DeleteMailAlias = 'DeleteMailAlias';
    case DeleteMailDomain = 'DeleteMailDomain';
    case DeleteMailbox = 'DeleteMailbox';
    case DeleteWebsite = 'DeleteWebsite';
    case Deploy = 'Deploy';
    case ExportWebsite = 'ExportWebsite';
    case ExtractArchive = 'ExtractArchive';
    // The import of an account from a different panel (docs/imports.md).
    case FetchImport = 'FetchImport';
    case ImportDatabase = 'ImportDatabase';
    case ImportFiles = 'ImportFiles';
    case ImportMail = 'ImportMail';
    case InstallDependencies = 'InstallDependencies';
    case InstallHostUpdates = 'InstallHostUpdates';
    case InstallWordPress = 'InstallWordPress';
    case MakeArchive = 'MakeArchive';
    case OpenTerminal = 'OpenTerminal';
    case PrepareMailNode = 'PrepareMailNode';
    case PruneBackups = 'PruneBackups';
    case Reboot = 'Reboot';
    case ReceiveDatabaseServer = 'ReceiveDatabaseServer';
    case ReceiveWebsite = 'ReceiveWebsite';
    case RemoveDomain = 'RemoveDomain';
    case Restore = 'Restore';
    case RestoreWordPress = 'RestoreWordPress';
    case RotateMeshKey = 'RotateMeshKey';
    case RunCommand = 'RunCommand';
    case RunCronJob = 'RunCronJob';
    case RunWpCli = 'RunWpCli';
    case ScanWebsite = 'ScanWebsite';
    case SendDatabaseServer = 'SendDatabaseServer';
    case SendWebsite = 'SendWebsite';
    case SetCertificates = 'SetCertificates';
    case SetCronJobs = 'SetCronJobs';
    case SetDatabaseServer = 'SetDatabaseServer';
    case SetDatabaseUser = 'SetDatabaseUser';
    case SetDnsSecondaries = 'SetDnsSecondaries';
    case SetDnsZones = 'SetDnsZones';
    case SetLimits = 'SetLimits';
    case SetMailAlias = 'SetMailAlias';
    case SetMailDomain = 'SetMailDomain';
    case SetMailNode = 'SetMailNode';
    case SetMailServer = 'SetMailServer';
    case SetMailSettings = 'SetMailSettings';
    case SetMailbox = 'SetMailbox';
    case SetMesh = 'SetMesh';
    case SetPacks = 'SetPacks';
    case SetPanelBrands = 'SetPanelBrands';
    case SetPanelCertificate = 'SetPanelCertificate';
    case SetRuntime = 'SetRuntime';
    case SetSftp = 'SetSftp';
    case SetWebsitePower = 'SetWebsitePower';
    case SetWebsiteSecurity = 'SetWebsiteSecurity';
    case UpdateAgent = 'UpdateAgent';
    case UpdateWordPress = 'UpdateWordPress';

    /** The steps that the agent reports for this action. */
    public function steps(): array
    {
        return match ($this) {
            self::ActivateRelease => ['Check the release', ...self::SetRuntime->steps()],
            self::AddDomain => ['Add edge proxy route', 'Check DNS', 'Get TLS certificate'],
            self::Backup => ['Snapshot files', 'Dump databases', 'Upload to object storage', 'Apply retention'],
            self::CheckWordPress => ['Check WordPress'],
            self::ConfigureHost => ['Save settings', 'Restart websites', 'Restart services'],
            self::CreateDatabase => ['Create database', 'Create user', 'Allow the website container only'],
            self::CreateWebsite => ['Create Unix user', 'Copy databases', 'Pull and check image signature', 'Start container in gVisor', 'Configure edge proxy', 'Get TLS certificate'],
            self::DeleteDatabase => ['Remove access of users', 'Remove database'],
            self::DeleteDatabaseServer => ['Stop database server', 'Remove data'],
            self::DeleteDatabaseUser => ['Remove user'],
            self::DeleteDnsZones => ['Remove zones'],
            self::DeleteImport => ['Remove archive'],
            self::DeleteMailAlias => ['Remove address'],
            self::DeleteMailDomain => ['Remove domain'],
            self::DeleteMailbox => ['Remove mailbox and its messages'],
            self::DeleteWebsite => ['Stop container', 'Remove edge proxy route', 'Remove Unix user'],
            // The steps after "Build" are the steps of SetRuntime: the agent starts the website
            // from the new release with no stop.
            self::Deploy => ['Get the code', 'Build', 'Migrate the database', ...self::SetRuntime->steps(), 'Remove old releases'],
            // An account export: the agent sends each part to the panel while it makes it.
            self::ExportWebsite => ['Send database dumps', 'Send files'],
            self::ExtractArchive => ['Extract the archive'],
            // A download for a server that only copies a part has no second step.
            self::FetchImport => ['Download archive', 'Read archive'],
            self::ImportDatabase => ['Check archive', 'Load dump'],
            self::ImportFiles => ['Check archive', 'Copy files'],
            self::ImportMail => ['Check archive', 'Copy messages'],
            // The agent installs the dependencies in a build container, then starts the website
            // again with no stop.
            self::InstallDependencies => ['Install dependencies', ...self::SetRuntime->steps()],
            self::InstallHostUpdates => ['Download packages', 'Install packages', 'Restart changed services only'],
            self::InstallWordPress => ['Check the website', 'Create database', 'Download WordPress', 'Install WordPress', 'Check WordPress'],
            self::MakeArchive => ['Make the archive'],
            self::OpenTerminal => ['Open the terminal'],
            self::PrepareMailNode => ['Make keys'],
            self::PruneBackups => ['Remove backups of deleted and moved websites', 'Remove unused data'],
            self::Reboot => ['Drain the edge proxy', 'Stop containers', 'Reboot', 'Start containers', 'Check all services'],
            self::ReceiveDatabaseServer => ['Prepare database server', 'Receive data', 'Start database server'],
            self::ReceiveWebsite => ['Create Unix user', 'Receive files', 'Pull and check image signature', 'Start container in gVisor', 'Configure edge proxy', 'Set SFTP access and cron jobs', 'Get TLS certificate'],
            self::RemoveDomain => ['Remove edge proxy route'],
            self::Restore => ['Download snapshot', 'Restore', 'Health check'],
            self::RestoreWordPress => ['Put back the restore point', 'Check WordPress'],
            self::RotateMeshKey => ['Make new key'],
            self::RunCommand => ['Run the command'],
            self::RunCronJob => ['Run the command'],
            self::RunWpCli => ['Run the command', 'Check WordPress'],
            self::ScanWebsite => ['Update malware signatures', 'Scan files'],
            self::SendDatabaseServer => ['Stop database server', 'Send data'],
            self::SendWebsite => ['Stop website', 'Send files'],
            self::SetCertificates => ['Load certificates', 'Check certificates'],
            self::SetCronJobs => ['Save the cron jobs'],
            self::SetDatabaseServer => ['Start database server'],
            self::SetDatabaseUser => ['Set user and password', 'Set access to databases'],
            self::SetDnsSecondaries => ['Start DNS server', 'Set secondary DNS servers'],
            self::SetDnsZones => ['Start DNS server', 'Write zones', 'Load zones'],
            self::SetLimits => ['Update cgroup limits'],
            self::SetMailAlias => ['Set address'],
            self::SetMailDomain => ['Add domain', 'Make DKIM keys'],
            self::SetMailNode => ['Make private network', 'Start cluster store', 'Start database'],
            self::SetMailServer => ['Start mail server', 'Start webmail', 'Open mail ports', 'Get TLS certificate'],
            self::SetMailSettings => ['Set spam filter', 'Set outbound relay'],
            self::SetMailbox => ['Set mailbox'],
            self::SetMesh => ['Set tunnels and firewall', 'Check tunnels'],
            self::SetPacks => ['Check the packs', 'Save the packs and the image policy', 'Load the runtimes'],
            self::SetPanelBrands => ['Set panel domains', 'Get TLS certificates'],
            self::SetPanelCertificate => ['Change certificate method', 'Get TLS certificate'],
            self::SetRuntime => ['Check image signature', 'Start new container on a copy', 'Health check', 'Switch traffic', 'Stop old container'],
            self::SetSftp => ['Set SFTP access'],
            self::SetWebsitePower => ['Change container state', 'Health check'],
            self::SetWebsiteSecurity => ['Set edge proxy rules'],
            self::UpdateAgent => ['Download agent binary', 'Check signature', 'Keep previous binary for rollback', 'Restart agent'],
            // When a step after "Save a restore point" fails, the agent puts back the restore point.
            self::UpdateWordPress => ['Check WordPress', 'Save a restore point', 'Update WordPress', 'Update plugins', 'Update themes', 'Health check'],
        };
    }

    /**
     * The actions whose result the panel keeps for the user: the summary of an archive
     * (agent/internal/sitefiles.Summary). The API gives it with the job.
     */
    public function keepsResult(): bool
    {
        return in_array($this, [self::MakeArchive, self::ExtractArchive], true);
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
