package core

import "time"

type DeviceID string
type ShareID string
type RevisionID string
type TombstoneID string
type TransferID string
type ConflictID string

type EntryType string

const (
	EntryFile               EntryType = "file"
	EntryDirectory          EntryType = "directory"
	EntrySymlinkUnsupported EntryType = "symlink_unsupported"
	EntryDeleted            EntryType = "deleted"
)

type Capability string

const (
	CapabilityList          Capability = "list"
	CapabilityRead          Capability = "read"
	CapabilityUpload        Capability = "upload"
	CapabilityModify        Capability = "modify"
	CapabilityRename        Capability = "rename"
	CapabilityDelete        Capability = "delete"
	CapabilityAccessHistory Capability = "access_history"
	CapabilityRestore       Capability = "restore_history"
	CapabilitySync          Capability = "sync"
	CapabilityRemoteAccess  Capability = "remote_access"
)

func ShareCapabilities() []Capability {
	return []Capability{
		CapabilityList,
		CapabilityRead,
		CapabilityUpload,
		CapabilityModify,
		CapabilityRename,
		CapabilityDelete,
		CapabilityAccessHistory,
		CapabilityRestore,
		CapabilitySync,
	}
}

func IsShareCapability(candidate Capability) bool {
	for _, capability := range ShareCapabilities() {
		if candidate == capability {
			return true
		}
	}
	return false
}

type Revision struct {
	ID               RevisionID
	ShareID          ShareID
	RelativePath     string
	EntryType        EntryType
	Size             int64
	ContentHash      string
	HashAlgorithm    string
	ParentRevisionID RevisionID
	OriginDeviceID   DeviceID
	Sequence         int64
	IsDeleted        bool
	CreatedAt        time.Time
}

type FileIndexEntry struct {
	ShareID           ShareID
	RelativePath      string
	EntryType         EntryType
	Size              int64
	ModifiedTime      time.Time
	CreationTime      time.Time
	FileIdentity      string
	ContentHash       string
	HashAlgorithm     string
	CurrentRevisionID RevisionID
	IsDeleted         bool
	DeletedAt         time.Time
	LastScannedAt     time.Time
}

type SharePermission struct {
	ShareID      ShareID
	DeviceID     DeviceID
	Capabilities map[Capability]bool
	LANOnly      bool
}
