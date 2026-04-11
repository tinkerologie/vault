package config

type CompressionType string

const (
	CompressionNone CompressionType = "none"
	CompressionGzip CompressionType = "gzip"
	CompressionZstd CompressionType = "zstd"
)

type BackupType string

const (
	BackupFull         BackupType = "full"
	BackupIncremental  BackupType = "incremental"
	BackupDifferential BackupType = "differential"
)

type VMBackupMode string

const (
	VMBackupSnapshot VMBackupMode = "snapshot"
	VMBackupCold     VMBackupMode = "cold"
)

type ContainerBackupMode string

const (
	ContainerStopAll  ContainerBackupMode = "stop_all"
	ContainerOneByOne ContainerBackupMode = "one_by_one"
)

type EncryptionType string

const (
	EncryptionNone EncryptionType = "none"
	EncryptionAge  EncryptionType = "age"
)

type StorageType string

const (
	StorageLocal StorageType = "local"
	StorageSMB   StorageType = "smb"
	StorageNFS   StorageType = "nfs"
	StorageSFTP  StorageType = "sftp"
)
